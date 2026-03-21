# mytool

A command-line tool for managing local development environments. Written in Python.

## Goals

- Create, start, stop, and destroy local dev environments
- Support Docker-based environments with custom configurations
- Provide a simple YAML-based configuration format
- Include comprehensive test coverage

## Non-Goals

- This is not a cloud deployment tool
- No GUI — CLI only
- No support for Kubernetes (just Docker)

## Development

### Setup

```bash
cd project
pip install -e ".[test]"
```

### Run tests

```bash
pytest tests/ -v
```
