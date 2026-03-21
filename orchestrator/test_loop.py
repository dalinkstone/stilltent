#!/usr/bin/env python3
"""
Test suite for orchestrator/loop.py

Tests the orchestrator's core functions:
- Circuit breaker state transitions
- Idle detection heuristics
- Response parsing and success detection
- Budget guard calculations
- Token extraction
"""

import json
import sys
import unittest
import time
from pathlib import Path

# Add parent directory to path for imports
sys.path.insert(0, str(Path(__file__).parent.parent))

from orchestrator.loop import (
    CircuitBreaker,
    response_indicates_success,
    _response_indicates_idle,
    _extract_result_field,
    _extract_token_usage,
    _calculate_iteration_cost,
)


class TestCircuitBreaker(unittest.TestCase):
    """Test CircuitBreaker state machine."""

    def test_initial_state_is_closed(self):
        """Circuit breaker starts in CLOSED state."""
        cb = CircuitBreaker()
        self.assertEqual(cb.state, cb.CLOSED)

    def test_closed_to_open_on_threshold(self):
        """Circuit opens after failure_threshold consecutive failures."""
        cb = CircuitBreaker(failure_threshold=3)
        self.assertEqual(cb.state, cb.CLOSED)
        
        # Record failures up to threshold
        for _ in range(3):
            cb.record_failure()
        
        self.assertEqual(cb.state, cb.OPEN)
        self.assertEqual(cb.consecutive_failures, 3)

    def test_half_open_transition(self):
        """Circuit transitions to HALF_OPEN after cooldown."""
        cb = CircuitBreaker(failure_threshold=1, open_duration=0.1)
        
        # Open the circuit
        cb.record_failure()
        self.assertEqual(cb.state, cb.OPEN)
        
        # Wait for cooldown
        time.sleep(0.15)
        
        # Probe should transition to HALF_OPEN
        self.assertTrue(cb.allow_request())
        self.assertEqual(cb.state, cb.HALF_OPEN)

    def test_half_open_to_closed_on_success(self):
        """HALF_OPEN closes on successful request."""
        cb = CircuitBreaker(failure_threshold=1, open_duration=0.01)
        
        # Open then wait for cooldown
        cb.record_failure()
        time.sleep(0.02)
        cb.allow_request()  # Transition to HALF_OPEN
        
        # Record success
        cb.record_success()
        self.assertEqual(cb.state, cb.CLOSED)
        self.assertEqual(cb.consecutive_failures, 0)

    def test_half_open_to_open_on_failure(self):
        """HALF_OPEN re-opens on failed probe with doubled cooldown."""
        cb = CircuitBreaker(failure_threshold=1, open_duration=0.01)
        
        # Open the circuit
        cb.record_failure()
        time.sleep(0.02)
        cb.allow_request()  # Transition to HALF_OPEN
        
        # Probe fails - should re-open with doubled cooldown
        cb.record_failure()
        self.assertEqual(cb.state, cb.OPEN)
        self.assertEqual(cb.open_duration, 0.02)  # Doubled


class TestResponseIndicatesSuccess(unittest.TestCase):
    """Test response_indicates_success function."""

    def test_success_with_tool_calls(self):
        """Response with tool_calls counts as success."""
        response = {
            "choices": [{
                "message": {
                    "content": "I'll create the project structure.",
                    "tool_calls": [{"id": "tool1", "type": "function"}]
                }
            }],
            "usage": {"prompt_tokens": 100, "completion_tokens": 50}
        }
        
        self.assertTrue(response_indicates_success(response))

    def test_success_with_tokens(self):
        """Response with token usage counts as success."""
        response = {
            "choices": [{
                "message": {"content": "Project structure created."}
            }],
            "usage": {"prompt_tokens": 100, "completion_tokens": 50}
        }
        
        self.assertTrue(response_indicates_success(response))

    def test_partial_success_with_text(self):
        """Non-empty text response without JSON counts as partial."""
        response = {
            "choices": [{
                "message": {"content": "I'm working on the project now."}
            }],
            "usage": {"prompt_tokens": 0, "completion_tokens": 0}
        }
        
        self.assertTrue(response_indicates_success(response))

    def test_failure_with_empty_response(self):
        """Empty response without tokens counts as failure."""
        response = {
            "choices": [{
                "message": {"content": ""}
            }],
            "usage": {"prompt_tokens": 0, "completion_tokens": 0}
        }
        
        self.assertFalse(response_indicates_success(response))


class TestResponseIndicatesIdle(unittest.TestCase):
    """Test _response_indicates_idle function."""

    def test_idle_with_skipped_result(self):
        """Explicit 'skipped' result indicates idle."""
        response = {
            "choices": [{
                "message": {"content": '{"result": "skipped", "summary": "No work found"}'}
            }]
        }
        
        self.assertTrue(_response_indicates_idle(response))

    def test_not_idle_with_pr_reference(self):
        """Response with PR reference indicates work was done."""
        response = {
            "choices": [{
                "message": {"content": "Created PR #42 for the new feature."}
            }]
        }
        
        self.assertFalse(_response_indicates_idle(response))

    def test_idle_with_no_work_phrase(self):
        """Idle phrase without work indicators indicates idle."""
        response = {
            "choices": [{
                "message": {"content": "No issues found. Repository is healthy."}
            }]
        }
        
        self.assertTrue(_response_indicates_idle(response))


class TestExtractResultField(unittest.TestCase):
    """Test _extract_result_field function."""

    def test_result_in_simple_json(self):
        """Extract result from simple JSON."""
        response = {
            "choices": [{
                "message": {"content": '{"result": "success", "iteration": 40}'}
            }]
        }
        
        self.assertEqual(_extract_result_field(response), "success")

    def test_result_in_nested_json(self):
        """Extract result from nested JSON structure."""
        response = {
            "choices": [{
                "message": {"content": '{"data": {"result": "partial"}}'}
            }]
        }
        
        self.assertEqual(_extract_result_field(response), "partial")

    def test_result_with_whitespace(self):
        """Extract result from JSON with extra whitespace."""
        response = {
            "choices": [{
                "message": {"content": '  {"result": "skipped"}  '}
            }]
        }
        
        self.assertEqual(_extract_result_field(response), "skipped")


class TestTokenExtraction(unittest.TestCase):
    """Test token extraction and cost calculation."""

    def test_extract_tokens_standard(self):
        """Extract tokens from standard OpenAI format."""
        response = {
            "usage": {"prompt_tokens": 1000, "completion_tokens": 500}
        }
        
        prompt, completion = _extract_token_usage(response)
        self.assertEqual(prompt, 1000)
        self.assertEqual(completion, 500)

    def test_extract_tokens_alternative_format(self):
        """Extract tokens from alternative format."""
        response = {
            "prompt_tokens": 2000,
            "completion_tokens": 1000
        }
        
        prompt, completion = _extract_token_usage(response)
        self.assertEqual(prompt, 2000)
        self.assertEqual(completion, 1000)


class TestCostCalculation(unittest.TestCase):
    """Test iteration cost calculation."""

    def test_cost_calculation(self):
        """Calculate cost correctly using pricing."""
        # $0.12/M input, $0.75/M output
        cost = _calculate_iteration_cost(1000000, 1000000)
        self.assertAlmostEqual(cost, 0.87)  # 0.12 + 0.75


if __name__ == "__main__":
    unittest.main()
