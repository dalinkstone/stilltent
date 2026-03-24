"""
Daytona Executor — Runs code inside a Daytona sandbox.
"""

from .client import DaytonaClient


class SandboxExecutor:
    """Executes commands and code inside a Daytona workspace."""

    def __init__(self, client: DaytonaClient, workspace_id: str):
        self.client = client
        self.workspace_id = workspace_id

    def run(self, command: str, timeout: int = 300) -> dict:
        """Execute a shell command in the sandbox."""
        # TODO: Implement via Daytona exec API
        return {"exit_code": -1, "stdout": "", "stderr": "not implemented"}

    def write_file(self, path: str, content: str) -> bool:
        """Write a file inside the sandbox."""
        # TODO: Implement via Daytona file API
        return False

    def read_file(self, path: str) -> str:
        """Read a file from the sandbox."""
        # TODO: Implement via Daytona file API
        return ""
