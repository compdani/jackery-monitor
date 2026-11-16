"""Where the Python stack finds assets shared with the Go server.

The Docker legacy image copies python/ next to web/ and models.json in /app;
in the repository those live one level up from python/.
"""

from pathlib import Path

HERE = Path(__file__).resolve().parent
APP_ROOT = HERE if (HERE / "web").is_dir() else HERE.parent
