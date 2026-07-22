"""Control-plane entrypoint: `python -m app.main` (or the Dockerfile CMD).

The LiveKit Agents worker is a separate process:
`python -m app.livekit_worker start` (see README).
"""

from __future__ import annotations

import uvicorn

from .config import load_settings
from .logging import configure_logging, get_logger

log = get_logger("main")


def main() -> None:
    settings = load_settings()
    configure_logging(settings.log_level)
    log.info("starting control plane", port=settings.port, backend=settings.agent_backend)
    uvicorn.run(
        "app.control_plane:create_app",
        factory=True,
        host="0.0.0.0",
        port=settings.port,
        log_level=settings.log_level,
    )


if __name__ == "__main__":
    main()
