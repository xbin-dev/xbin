# Dice

An MCP server (streamable HTTP at `POST /mcp`) with one tool:

- `roll_dice {sides?: 2–1000 (6), count?: 1–20 (1)}` → `rolled 1d6: 4 (total 4)`.

`GET /roll` rolls one six-sided die: `{"text": "rolled 1d6: 4 (total 4)"}`. Nothing here changes anything.
