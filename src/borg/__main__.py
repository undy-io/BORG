# src/borg/__main__.py
import argparse
import os

import uvicorn

from borg import main


def _parse_args():
    parser = argparse.ArgumentParser(description="Run the BORG router.")
    parser.add_argument(
        "--config", "-c", default=os.getenv("PROXY_CONFIG", "config.yaml")
    )
    parser.add_argument("--host", default="0.0.0.0")
    parser.add_argument("--port", type=int, default=int(os.getenv("PORT", 8000)))
    parser.add_argument("--reload", action="store_true")
    return parser.parse_args()


def run():
    args = _parse_args()
    os.environ["PROXY_CONFIG"] = args.config
    if args.reload:
        uvicorn.run(
            "borg.main:create_app",
            factory=True,
            host=args.host,
            port=args.port,
            reload=True,
        )
        return

    uvicorn.run(main.create_app(args.config), host=args.host, port=args.port)


if __name__ == "__main__":
    run()
