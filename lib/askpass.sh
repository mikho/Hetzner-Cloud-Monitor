#!/bin/sh
# OpenSSH asks this script for the password or key passphrase. It reads it
# from a 0600 file in a private temp dir that lives for one operation.
exec cat "$HDASH_SECRET_FILE"
