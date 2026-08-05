#!/bin/bash
# verify-macos-bundle.sh — verify provisioning profiles inside AgentSH.app.
#
# AMFI refuses to exec any binary signed with a restricted entitlement unless
# its bundle embeds a provisioning profile granting that entitlement. Neither
# codesign --verify nor notarization checks this, so a broken bundle only
# fails on user machines (issue #436). This script is the release gate for
# that condition, and is equally runnable against an installed
# /Applications/AgentSH.app or a mounted DMG when triaging user reports.
#
# Usage: scripts/verify-macos-bundle.sh /path/to/AgentSH.app
#
# Requires macOS 12+ (codesign --xml needs 11+, plutil -extract raw needs 12+).
#
# Exit codes: 0 all checks pass (warnings allowed), 1 check failures,
# 2 usage/environment error.
#
# Deliberately no `set -e`: failures are collected and all reported at once.
set -u

PLISTBUDDY=/usr/libexec/PlistBuddy
SYSEXT_REL="Contents/Library/SystemExtensions/ai.canyonroad.agentsh.SysExt.systemextension"
# Restricted entitlements this project uses. Apple publishes no
# machine-readable classification; extend this list when a new restricted
# entitlement is adopted.
RESTRICTED_ENTITLEMENTS=(
  com.apple.developer.endpoint-security.client
  com.apple.developer.networking.networkextension
  com.apple.developer.system-extension.install
)
EXPIRY_WARN_DAYS=90

failures=()
warnings=()
passes=0

fail() { failures+=("$1"); printf 'FAIL  %s\n' "$1"; }
warn() { warnings+=("$1"); printf 'WARN  %s\n' "$1"; }
pass() { passes=$((passes + 1)); printf 'ok    %s\n' "$1"; }
die()  { printf 'error: %s\n' "$1" >&2; exit 2; }

[ "$(uname -s)" = "Darwin" ] || die "requires macOS (needs security/codesign/plutil/PlistBuddy)"
for tool in security codesign plutil date "$PLISTBUDDY"; do
  command -v "$tool" >/dev/null 2>&1 || die "required tool not found: $tool"
done
[ $# -eq 1 ] || die "usage: $0 /path/to/AgentSH.app"

APP="${1%/}"
[ -f "$APP/Contents/Info.plist" ] || die "not an app bundle (no Contents/Info.plist): $APP"

WORKDIR="$(mktemp -d)"
trap 'rm -rf "$WORKDIR"' EXIT

# check_bundle <label> <bundle-path>
check_bundle() {
  local label="$1" bundle="$2"
  local profile="$bundle/Contents/embedded.provisionprofile"
  local decoded="$WORKDIR/$label-profile.plist"
  local ents="$WORKDIR/$label-entitlements.plist"
  local ent

  printf '\n== %s: %s\n' "$label" "$bundle"

  if [ ! -d "$bundle" ]; then
    fail "$label: bundle not found at $bundle"
    return
  fi

  # Signed entitlements, needed for the profile cross-check.
  local have_ents=0
  if codesign -d --entitlements "$ents" --xml "$bundle" 2>/dev/null && [ -s "$ents" ]; then
    have_ents=1
  else
    fail "$label: cannot read signed entitlements (unsigned, ad-hoc, or signed with no entitlements)"
  fi
  local sig_team
  sig_team="$(codesign -dvv "$bundle" 2>&1 | awk -F= '/^TeamIdentifier=/{print $2; exit}')"

  # Check 6: each bundle must actually CLAIM its load-bearing restricted
  # entitlements. Check 5 alone (claimed ⊆ granted) false-passes when a
  # signing regression drops entitlements from the signature entirely —
  # the wrong --entitlements file ships a sysext that cannot create an ES
  # client, silently (same failure character as #436).
  local required_ents=""
  case "$label" in
    app)    required_ents="com.apple.developer.system-extension.install" ;;
    sysext) required_ents="com.apple.developer.endpoint-security.client com.apple.developer.networking.networkextension" ;;
  esac
  if [ "$have_ents" -eq 1 ]; then
    for ent in $required_ents; do
      if "$PLISTBUDDY" -c "Print :$ent" "$ents" >/dev/null 2>&1; then
        pass "$label: signature claims required entitlement $ent"
      else
        fail "$label: signature does NOT claim required entitlement $ent (wrong entitlements file at signing?)"
      fi
    done
  fi

  # Check 1: profile present and decodable.
  if [ ! -f "$profile" ]; then
    fail "$label: missing $profile"
    # With no profile, every claimed restricted entitlement is ungranted.
    if [ "$have_ents" -eq 1 ]; then
      for ent in "${RESTRICTED_ENTITLEMENTS[@]}"; do
        if "$PLISTBUDDY" -c "Print :$ent" "$ents" >/dev/null 2>&1; then
          fail "$label: restricted entitlement $ent claimed by signature but no profile grants it (AMFI will reject at exec)"
        fi
      done
    fi
    return
  fi
  if ! security cms -D -i "$profile" > "$decoded" 2>/dev/null || [ ! -s "$decoded" ]; then
    fail "$label: embedded.provisionprofile cannot be decoded (corrupt?)"
    return
  fi
  pass "$label: embedded.provisionprofile present and decodable"

  # Check 2: identity — profile app ID matches team + bundle ID, and the
  # signature's team matches the profile's. TeamIdentifier is a plist array.
  local bundle_id profile_appid profile_team
  bundle_id="$("$PLISTBUDDY" -c 'Print :CFBundleIdentifier' "$bundle/Contents/Info.plist" 2>/dev/null)"
  profile_appid="$("$PLISTBUDDY" -c 'Print :Entitlements:com.apple.application-identifier' "$decoded" 2>/dev/null)"
  profile_team="$("$PLISTBUDDY" -c 'Print :TeamIdentifier:0' "$decoded" 2>/dev/null)"

  if [ -z "$bundle_id" ]; then
    fail "$label: cannot read CFBundleIdentifier from $bundle/Contents/Info.plist"
  elif [ "$profile_appid" != "$profile_team.$bundle_id" ]; then
    fail "$label: profile application-identifier '$profile_appid' != expected '$profile_team.$bundle_id'"
  else
    pass "$label: profile application-identifier matches $profile_appid"
  fi

  if [ -z "$sig_team" ] || [ "$sig_team" = "not set" ]; then
    fail "$label: code signature has no team identifier (ad-hoc or unsigned?)"
  elif [ "$sig_team" != "$profile_team" ]; then
    fail "$label: signature team '$sig_team' != profile team '$profile_team'"
  else
    pass "$label: signature team matches profile team ($sig_team)"
  fi

  # Check 3: must be a Developer ID distribution profile. A device-limited
  # development profile passes on the build machine and fails everywhere else.
  local all_devices
  all_devices="$("$PLISTBUDDY" -c 'Print :ProvisionsAllDevices' "$decoded" 2>/dev/null)"
  if [ "$all_devices" = "true" ]; then
    pass "$label: ProvisionsAllDevices=true"
  else
    fail "$label: not a ProvisionsAllDevices distribution profile (got '${all_devices:-absent}')"
  fi

  # Check 4: expiry. plutil raw prints plist dates as ISO8601 UTC.
  local expiry_iso expiry_epoch now_epoch
  expiry_iso="$(plutil -extract ExpirationDate raw -o - "$decoded" 2>/dev/null)"
  if [ -z "$expiry_iso" ]; then
    fail "$label: profile has no readable ExpirationDate"
  else
    expiry_epoch="$(date -j -u -f '%Y-%m-%dT%H:%M:%SZ' "$expiry_iso" +%s 2>/dev/null || echo 0)"
    now_epoch="$(date -u +%s)"
    if [ "$expiry_epoch" -eq 0 ]; then
      fail "$label: cannot parse ExpirationDate '$expiry_iso'"
    elif [ "$expiry_epoch" -le "$now_epoch" ]; then
      fail "$label: profile expired on $expiry_iso"
    elif [ "$expiry_epoch" -le $((now_epoch + EXPIRY_WARN_DAYS * 86400)) ]; then
      warn "$label: profile expires within ${EXPIRY_WARN_DAYS} days ($expiry_iso)"
    else
      pass "$label: profile valid until $expiry_iso"
    fi
  fi

  # Check 5: every restricted entitlement the signature claims must be
  # granted by the profile. Key-presence, not value equality —
  # networking.networkextension is array-valued on both sides.
  if [ "$have_ents" -eq 1 ]; then
    for ent in "${RESTRICTED_ENTITLEMENTS[@]}"; do
      if "$PLISTBUDDY" -c "Print :$ent" "$ents" >/dev/null 2>&1; then
        if "$PLISTBUDDY" -c "Print :Entitlements:$ent" "$decoded" >/dev/null 2>&1; then
          pass "$label: restricted entitlement $ent granted by profile"
        else
          fail "$label: restricted entitlement $ent claimed by signature but NOT granted by profile (AMFI will reject at exec)"
        fi
      fi
    done
  fi
}

check_bundle "app" "$APP"
check_bundle "sysext" "$APP/$SYSEXT_REL"

printf '\n== summary: %d ok, %d warning(s), %d failure(s)\n' \
  "$passes" "${#warnings[@]}" "${#failures[@]}"
if [ "${#failures[@]}" -gt 0 ]; then
  printf 'failure: %s\n' "${failures[@]}"
  exit 1
fi
exit 0
