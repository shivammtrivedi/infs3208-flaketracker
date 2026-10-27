#!/usr/bin/env bash
# Builds the single ZIP that is uploaded to Blackboard: source code, README with deployment
# instructions, and the seed generator (the project's data).
#
#   scripts/make-submission-zip.sh <FirstName_LastName_StudentNo>
#   e.g. scripts/make-submission-zip.sh Jane_Citizen_4xxxxxxx
#
# The result is dist/<name>.zip, and unzipping it creates one folder called <name>/.
#
# WHICH FILES GO IN: everything git knows about (committed OR new) minus whatever .gitignore
# excludes. That leaves out, by design:
#   .env and other secrets, web/node_modules, web/dist and build output, *.zip,
#   the generated deploy/k8s/overlays/_release/, and the private planning file.
# Nothing here stages or commits anything: it only READS the list of files.

set -euo pipefail

NAME="${1:?usage: make-submission-zip.sh <FirstName_LastName_StudentNo>}"

# Always work from the repo root, wherever the script is started from.
cd "$(dirname "${BASH_SOURCE[0]}")/.."

mkdir -p dist
OUT="dist/$NAME.zip"
rm -f "$OUT"          # start from scratch so no old files linger

# Safety net: refuse to continue if a secret or dependency folder would be included.
#   --cached           files git tracks          --others          new, untracked files
#   --exclude-standard apply .gitignore          -z                NUL-separated (safe for spaces)
FILES="$(git ls-files --cached --others --exclude-standard -z | tr '\0' '\n')"
BAD="$(printf '%s\n' "$FILES" | grep -E '(^|/)(\.env|node_modules/|.*\.pem|.*\.key|.*\.zip)$|(^|/)\.env\.[^e]' || true)"
if [ -n "$BAD" ]; then
  echo "Refusing to zip: these files look like secrets or build output:" >&2
  echo "$BAD" >&2
  exit 1
fi

# Python's zipfile writes the ZIP (Git Bash has no 'zip' program). Reads the file list on stdin.
#   - every path is stored under the folder <name>/ with forward slashes (works on Windows, Mac, Linux)
#   - *.sh files are marked executable so ./scripts/... works after unzipping on Linux/Mac
printf '%s\n' "$FILES" | python -I -c '
import sys, zipfile, os
name, out = sys.argv[1], sys.argv[2]
with zipfile.ZipFile(out, "w", zipfile.ZIP_DEFLATED) as z:
    for path in sys.stdin.read().splitlines():
        if not path or not os.path.isfile(path):
            continue                       # skip deleted files git still lists
        info = zipfile.ZipInfo.from_file(path, f"{name}/{path}")
        info.compress_type = zipfile.ZIP_DEFLATED
        mode = 0o755 if path.endswith(".sh") else 0o644
        info.external_attr = mode << 16    # unix permission bits live in the top 16 bits
        with open(path, "rb") as f:
            z.writestr(info, f.read())
' "$NAME" "$OUT"

# Report what was made: size, number of files, and the top-level contents.
echo "Created $OUT ($(du -h "$OUT" | cut -f1), $(python -I -c 'import sys,zipfile;print(len(zipfile.ZipFile(sys.argv[1]).namelist()))' "$OUT") files)"
