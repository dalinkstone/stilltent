"""Tests for mytool CLI."""

import subprocess
import sys
from unittest.mock import patch


def test_help():
    """Test that help message displays."""
    result = subprocess.run(
        [sys.executable, "-m", "mytool.cli", "--help"],
        capture_output=True,
        text=True,
        cwd="/workspace/repo/project"
    )
    assert result.returncode == 0
    assert "mytool" in result.stdout
    assert "create" in result.stdout


def test_no_command_shows_help():
    """Test that running without command shows help."""
    result = subprocess.run(
        [sys.executable, "-m", "mytool.cli"],
        capture_output=True,
        text=True,
        cwd="/workspace/repo/project"
    )
    assert result.returncode == 0
    assert "mytool" in result.stdout


def test_invalid_command():
    """Test that invalid command returns error."""
    result = subprocess.run(
        [sys.executable, "-m", "mytool.cli", "invalid"],
        capture_output=True,
        text=True,
        cwd="/workspace/repo/project"
    )
    assert result.returncode != 0


def test_main_function():
    """Test that main function can be called."""
    from mytool.cli import main
    
    with patch("sys.argv", ["mytool", "--help"]):
        with patch("sys.exit") as mock_exit:
            result = main()
            mock_exit.assert_called_once_with(0)
