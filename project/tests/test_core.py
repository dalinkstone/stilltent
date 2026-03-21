"""Tests for mytool."""

import pytest
from mytool.core import MyTool


class TestMyTool:
    """Tests for the MyTool class."""
    
    @pytest.fixture
    def tool(self):
        """Create a MyTool instance for testing."""
        return MyTool(verbose=True)
    
    def test_initialization(self, tool):
        """Test that MyTool initializes correctly."""
        assert tool.verbose is True
        assert tool.base_dir.name == ".mytool"
    
    def test_default_config_generation(self, tool):
        """Test that default config is generated correctly."""
        config = tool._default_config("test")
        assert "name: test" in config
        assert "docker:" in config
        assert "python:3.11-slim" in config
    
    def test_log_only_shows_when_verbose(self, capsys):
        """Test that _log only outputs when verbose is True."""
        verbose_tool = MyTool(verbose=True)
        quiet_tool = MyTool(verbose=False)
        
        verbose_tool._log("test message")
        captured_verbose = capsys.readouterr()
        assert "test message" in captured_verbose.out
        
        quiet_tool._log("test message")
        captured_quiet = capsys.readouterr()
        assert "test message" not in captured_quiet.out
