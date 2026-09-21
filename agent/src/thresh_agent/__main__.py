"""Run the agent sidecar: python -m thresh_agent"""

import os

import uvicorn

from .app import AGENT_VERSION, create_app


def main() -> None:
    host = os.environ.get("AGENT_HOST", "127.0.0.1")
    port = int(os.environ.get("AGENT_PORT", "8001"))
    uvicorn.run(create_app(AGENT_VERSION), host=host, port=port)


if __name__ == "__main__":
    main()
