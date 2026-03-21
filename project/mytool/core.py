"""Core functionality for mytool."""

import os
from pathlib import Path
from typing import Optional


class MyTool:
    """Main class for managing local development environments."""
    
    def __init__(self, verbose: bool = False):
        """Initialize the tool.
        
        Args:
            verbose: Enable verbose output if True.
        """
        self.verbose = verbose
        self.base_dir = Path.home() / ".mytool"
        self.environments_dir = self.base_dir / "environments"
    
    def _log(self, message: str) -> None:
        """Print message if verbose mode is enabled."""
        if self.verbose:
            print(f"[mytool] {message}")
    
    def create(self, name: str, config: str) -> None:
        """Create a new development environment.
        
        Args:
            name: Environment name.
            config: Path to configuration file.
        """
        self._log(f"Creating environment '{name}'")
        env_dir = self.environments_dir / name
        env_dir.mkdir(parents=True, exist_ok=True)
        
        # Copy config if it exists
        config_path = Path(config)
        if config_path.exists():
            (env_dir / "config.yaml").write_text(config_path.read_text())
            self._log(f"Config copied from {config}")
        
        # Create default config if none exists
        default_config = env_dir / "config.yaml"
        if not default_config.exists():
            default_config.write_text(self._default_config(name))
            self._log(f"Default config created at {default_config}")
        
        # Create environment metadata
        metadata = env_dir / "metadata.yaml"
        metadata.write_text(f"name: {name}\ncreated: {self._current_timestamp()}\n")
        self._log(f"Environment '{name}' created at {env_dir}")
    
    def start(self, name: str, config: str) -> None:
        """Start a development environment.
        
        Args:
            name: Environment name.
            config: Path to configuration file.
        """
        self._log(f"Starting environment '{name}'")
        # TODO: Implement Docker start logic
        print(f"Environment '{name}' would be started")
    
    def stop(self, name: str, config: str) -> None:
        """Stop a development environment.
        
        Args:
            name: Environment name.
            config: Path to configuration file.
        """
        self._log(f"Stopping environment '{name}'")
        # TODO: Implement Docker stop logic
        print(f"Environment '{name}' would be stopped")
    
    def destroy(self, name: str, config: str) -> None:
        """Destroy a development environment.
        
        Args:
            name: Environment name.
            config: Path to configuration file.
        """
        self._log(f"Destroying environment '{name}'")
        env_dir = self.environments_dir / name
        if env_dir.exists():
            import shutil
            shutil.rmtree(env_dir)
            self._log(f"Environment '{name}' destroyed")
        else:
            print(f"Environment '{name}' not found", file=__import__('sys').stderr)
    
    def _default_config(self, name: str) -> str:
        """Return default configuration YAML."""
        return f"""# Default configuration for {name}
name: {name}
docker:
  image: python:3.11-slim
  ports:
    - "8080:8080"
  volumes:
    - .:/app
  environment:
    - PYTHONUNBUFFERED=1
"""
    
    def _current_timestamp(self) -> str:
        """Return current timestamp as ISO format string."""
        from datetime import datetime
        return datetime.now().isoformat()
