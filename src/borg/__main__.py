# src/borg/__main__.py
import argparse
import os
import uvicorn
from borg import main

def _parse_args():
    parser = argparse.ArgumentParser(description='Run the BORG router.')
    parser.add_argument('--config', '-c', default=os.getenv("PROXY_CONFIG", "config.yaml"))
    parser.add_argument('--host', default='0.0.0.0')
    parser.add_argument('--port', type=int, default=int(os.getenv("PORT", 8000)))
    parser.add_argument('--reload', action='store_true')
    return parser.parse_args()

def run():
    args = _parse_args()
    main.configure(args.config)  # ensure config path is set
    uvicorn.run("borg.main:app", host=args.host, port=args.port, reload=args.reload)

if __name__ == "__main__":
    run()