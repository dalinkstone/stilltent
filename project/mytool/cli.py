"""Command-line interface for mytool."""

import argparse
import sys

from mytool.core import MyTool


def main() -> int:
    """Main entry point for the CLI."""
    parser = argparse.ArgumentParser(
        prog="mytool",
        description="A command-line tool for managing local development environments"
    )
    parser.add_argument(
        "command",
        nargs="?",
        choices=["create", "start", "stop", "destroy"],
        help="Command to execute"
    )
    parser.add_argument(
        "--name",
        "-n",
        default="default",
        help="Environment name (default: default)"
    )
    parser.add_argument(
        "--config",
        "-c",
        default="env.yaml",
        help="Configuration file path (default: env.yaml)"
    )
    parser.add_argument(
        "--verbose",
        "-v",
        action="store_true",
        help="Enable verbose output"
    )
    
    args = parser.parse_args()
    
    if not args.command:
        parser.print_help()
        return 0
    
    tool = MyTool(verbose=args.verbose)
    
    try:
        match args.command:
            case "create":
                tool.create(args.name, args.config)
            case "start":
                tool.start(args.name, args.config)
            case "stop":
                tool.stop(args.name, args.config)
            case "destroy":
                tool.destroy(args.name, args.config)
        return 0
    except Exception as e:
        print(f"Error: {e}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
