"""Copy the canonical native user schema for external tooling."""
from pathlib import Path
import sys
root = Path(__file__).resolve().parents[1]
schema = (root / 'booksvr/internal/httpapi/testdata/user_schema.sql').read_text(encoding='utf8')
if len(sys.argv) > 1:
    Path(sys.argv[1]).write_text(schema, encoding='utf8')
else:
    print(schema)
