# shellcheck shell=bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# The tool images the test suites use, sourced by their runners. They are
# pinned by version and digest: with "latest" a suite would run against
# whatever was published last, and a result could not be repeated. Set the
# variables to try another version.
PEBBLE_IMAGE=${PEBBLE_IMAGE:-ghcr.io/letsencrypt/pebble:2.10.3@sha256:0c81f05db371f436c97fb894bd6d2ab8fc9c8e2ce40f8bef8a8e15941bcd75ef}
RCLONE_IMAGE=${RCLONE_IMAGE:-rclone/rclone:1.76.0@sha256:7cbeec24a44f206067597c0368be4de90ae19a64d5113c7d1938bd737a81e466}
export PEBBLE_IMAGE RCLONE_IMAGE
