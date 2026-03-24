"""
Daytona Tester — Runs tests entirely inside a Daytona sandbox.

Ensures tests execute in an isolated environment matching production,
not on the host machine.
"""

from .executor import SandboxExecutor


class SandboxTester:
    """Test runner that executes within Daytona sandbox."""

    def __init__(self, executor: SandboxExecutor):
        self.executor = executor

    def run_tests(self, test_command: str = "make test") -> dict:
        """Run the test suite in the sandbox and return results."""
        result = self.executor.run(test_command)
        return {
            "passed": result["exit_code"] == 0,
            "exit_code": result["exit_code"],
            "stdout": result["stdout"],
            "stderr": result["stderr"],
        }

    def run_lint(self, lint_command: str = "make lint") -> dict:
        """Run linters in the sandbox."""
        result = self.executor.run(lint_command)
        return {
            "passed": result["exit_code"] == 0,
            "output": result["stdout"],
        }
