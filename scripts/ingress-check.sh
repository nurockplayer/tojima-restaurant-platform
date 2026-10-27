#!/bin/sh
# Ingress/security check for a deployed public origin (go-live gate). It sends
# no credentials and changes no business data; the only write it causes is the
# server's short-lived staff login transaction from GET /staff/auth/login,
# which expires unused like any abandoned sign-in.
#
# Usage: sh scripts/ingress-check.sh [--allow-loopback-http] [--db-host HOST] <public-origin>
#
# The operator runs this against the deployed origin before go-live. It reads
# only status codes and headers: no cookies, no credentials, no bursts, no
# redirect following, and never prints a response body or cookie value. The D8
# rate limits are a separate probe; the NEXT line prints that command.
#
# Each check prints one line: PASS|WARN|FAIL <name> <short reason>. Only FAIL
# affects the exit status (1); usage errors exit 2.
set -u

OPT_ALLOW_LOOPBACK_HTTP=false
OPT_DB_HOST=
ORIGIN=
PORT_NUMBER=
HSTS_MIN_MAX_AGE=15552000
TX_COOKIE=__Host-tojima_staff_tx
BROWSER_COOKIE=__Host-tojima_staff_browser

usage() {
    cat >&2 <<'USAGE'
usage: sh scripts/ingress-check.sh [--allow-loopback-http] [--db-host HOST] <public-origin>

  <public-origin>        https://host[:port], no path (the deployed public origin)
  --allow-loopback-http  permit http:// for 127.0.0.1, localhost or [::1] only
  --db-host HOST         also require HOST:5432 to be unreachable from this machine;
                         run from outside the deployment network with the
                         host's public address (a private address is reachable
                         by design and will FAIL)

  Checks: tls http-redirect hsts healthz spa-headers csp no-listing
          staff-origin staff-cookie [db-port]
  Exit 2 on usage errors, exit 1 if any check FAILs (WARN does not fail).
USAGE
    exit 2
}

while [ $# -gt 0 ]; do
    case $1 in
        --allow-loopback-http) OPT_ALLOW_LOOPBACK_HTTP=true; shift ;;
        --db-host)
            [ $# -ge 2 ] || usage
            OPT_DB_HOST=$2
            [ -n "$OPT_DB_HOST" ] || usage
            shift 2 ;;
        -*) usage ;;
        *)
            [ -z "$ORIGIN" ] || usage
            ORIGIN=$1
            shift ;;
    esac
done
[ -n "$ORIGIN" ] || usage

# Parse "<origin>" as scheme://host[:port] with no path, query, fragment or
# userinfo. A lone trailing slash is the same origin and is normalized away.
case $ORIGIN in
    https://*) SCHEME=https; AUTHORITY=${ORIGIN#https://} ;;
    http://*) SCHEME=http; AUTHORITY=${ORIGIN#http://} ;;
    *) usage ;;
esac
AUTHORITY=${AUTHORITY%/}
case $AUTHORITY in
    ''|*/*|*'?'*|*'#'*|*@*|*' '*) usage ;;
esac
case $AUTHORITY in
    \[*\]:*) HOST=${AUTHORITY%:*}; PORT=${AUTHORITY##*:} ;;
    \[*\]) HOST=$AUTHORITY; PORT= ;;
    *:*) HOST=${AUTHORITY%%:*}; PORT=${AUTHORITY#*:} ;;
    *) HOST=$AUTHORITY; PORT= ;;
esac
[ -n "$HOST" ] || usage
case $HOST in
    '['*']')
        host_inner=${HOST#'['}
        host_inner=${host_inner%']'}
        case $host_inner in ''|*[!A-Za-z0-9.:]*) usage ;; esac ;;
    *[!A-Za-z0-9.:_-]*) usage ;;
esac
if [ -n "$PORT" ]; then
    case $PORT in *[!0-9]*) usage ;; esac
    PORT_NUMBER=$(printf '%s' "$PORT" | sed 's/^0*//')
    if [ -z "$PORT_NUMBER" ] || [ "$PORT_NUMBER" -lt 1 ] || [ "$PORT_NUMBER" -gt 65535 ]; then
        echo "ingress-check: port must be 1-65535, got $PORT" >&2
        usage
    fi
fi

if [ "$SCHEME" = http ]; then
    if [ "$OPT_ALLOW_LOOPBACK_HTTP" != true ]; then
        echo 'ingress-check: http:// requires --allow-loopback-http' >&2
        usage
    fi
    case $HOST in
        127.0.0.1|localhost|'[::1]') ;;
        *)
            echo "ingress-check: http:// is allowed only for loopback hosts, not $HOST" >&2
            usage ;;
    esac
    MODE=loopback
else
    MODE=https
fi

BASE="$SCHEME://$AUTHORITY"

WORK=$(mktemp -d) || { echo 'ingress-check: mktemp -d failed' >&2; exit 1; }
cleanup() { rm -rf "$WORK"; }
trap cleanup EXIT HUP INT TERM

FAILED=0
report() { # report STATUS NAME REASON
    printf '%s %s %s\n' "$1" "$2" "$3"
    if [ "$1" = FAIL ]; then
        FAILED=$((FAILED + 1))
    fi
    return 0
}

HTTP_CODE=
HTTP_RC=0
fetch() { # fetch PREFIX METHOD URL [curl args...]
    # One request, no cookies and no redirect following; headers and body go to
    # files so nothing sensitive is echoed. Sets HTTP_CODE and HTTP_RC.
    prefix=$1 method=$2 url=$3
    shift 3
    : >"$prefix.body"
    : >"$prefix.headers"
    # -q must stay first: it stops ~/.curlrc from injecting cookies, -k or -L.
    HTTP_CODE=$(curl -q -sS --max-time 10 -o "$prefix.body" -D "$prefix.headers" \
        -w '%{http_code}' -X "$method" "$@" "$url" 2>"$prefix.err")
    HTTP_RC=$?
    if [ "$HTTP_RC" -ne 0 ]; then
        HTTP_CODE=
    fi
    return 0
}

header_value() { # header_value FILE NAME -> first value, case-insensitive
    tr -d '\r' <"$1" | awk -v want="$(printf '%s' "$2" | tr 'A-Z' 'a-z')" '
        index(tolower($0), want ":") == 1 {
            sub(/^[^:]*:[ \t]*/, "")
            print
            exit
        }'
}

header_all() { # header_all FILE NAME -> every value, case-insensitive
    tr -d '\r' <"$1" | awk -v want="$(printf '%s' "$2" | tr 'A-Z' 'a-z')" '
        index(tolower($0), want ":") == 1 {
            sub(/^[^:]*:[ \t]*/, "")
            print
        }'
}

lower() { printf '%s' "$1" | tr 'A-Z' 'a-z'; }

# /healthz serves the tls and healthz checks; / serves hsts, spa-headers and csp.
fetch "$WORK/health" GET "$BASE/healthz"
HEALTH_CODE=$HTTP_CODE
HEALTH_RC=$HTTP_RC
fetch "$WORK/root" GET "$BASE/"
ROOT_CODE=$HTTP_CODE
ROOT_RC=$HTTP_RC

# 1. tls: the deployed origin must complete a verified TLS handshake.
if [ "$MODE" != https ]; then
    report WARN tls 'loopback http, TLS not checked'
elif [ -n "$HEALTH_CODE" ]; then
    report PASS tls 'TLS handshake succeeded'
else
    case $HEALTH_RC in
        60) reason='certificate verification failed' ;;
        35|51|53|54|58|59|64|66|77|80|83|90|91) reason='TLS negotiation failed' ;;
        *) reason='connection failed' ;;
    esac
    report FAIL tls "$reason (curl exit $HEALTH_RC)"
fi

# 2. http-redirect: plain http on port 80 must redirect to the https origin.
if [ "$MODE" != https ]; then
    report WARN http-redirect 'loopback http, redirect not checked'
else
    fetch "$WORK/redirect" GET "http://$HOST/healthz"
    if [ "$HTTP_RC" -eq 7 ]; then
        report WARN http-redirect 'port 80 closed'
    elif [ -z "$HTTP_CODE" ]; then
        report FAIL http-redirect "no response on port 80 (curl exit $HTTP_RC)"
    else
        case $HTTP_CODE in
            301|302|307|308)
                location=$(lower "$(header_value "$WORK/redirect.headers" Location)")
                case $location in
                    '')
                        report FAIL http-redirect 'redirect missing Location' ;;
                    https://*)
                        # Compare the whole authority: a bare prefix match would also
                        # accept https://origin:443@evil.invalid or origin.evil.
                        location_auth=${location#https://}
                        location_auth=${location_auth%%/*}
                        location_auth=${location_auth%%\?*}
                        location_auth=${location_auth%%\#*}
                        host_l=$(lower "$HOST")
                        redirect_ok=false
                        # The redirect must land on the configured origin's port.
                        want_port=443
                        [ -n "$PORT" ] && want_port=$PORT_NUMBER
                        case $location_auth in
                            *@*) ;;
                            "$host_l:$want_port") redirect_ok=true ;;
                            "$host_l") [ "$want_port" = 443 ] && redirect_ok=true ;;
                        esac
                        if [ "$redirect_ok" = true ]; then
                            report PASS http-redirect "redirects to https://$location_auth"
                        else
                            report FAIL http-redirect 'Location is not the https origin'
                        fi ;;
                    *)
                        report FAIL http-redirect 'Location is not https' ;;
                esac ;;
            *)
                report FAIL http-redirect "status $HTTP_CODE (want a redirect)" ;;
        esac
    fi
fi

# 3. hsts: the ingress must pin HTTPS for at least 180 days.
if [ "$MODE" != https ]; then
    report WARN hsts 'loopback http, HSTS not checked'
elif [ -z "$ROOT_CODE" ]; then
    report FAIL hsts "GET / failed (curl exit $ROOT_RC)"
else
    hsts=$(header_value "$WORK/root.headers" Strict-Transport-Security)
    if [ -z "$hsts" ]; then
        report FAIL hsts 'missing Strict-Transport-Security'
    else
        # Only the leading max-age directive counts (an extension x-max-age must
        # never stand in for it).
        max_age=$(lower "$hsts" | tr ';' '\n' |
            awk '{ sub(/^[[:space:]]*/, "") } index($0, "max-age=") == 1 {
                sub(/^max-age=/, ""); sub(/[[:space:]].*$/, ""); print; exit }')
        if [ -z "$max_age" ]; then
            report FAIL hsts 'missing max-age'
        elif ! printf '%s' "$max_age" | grep -Eq '^[0-9]{1,15}$'; then
            report FAIL hsts 'malformed max-age'
        elif [ "$max_age" -lt "$HSTS_MIN_MAX_AGE" ]; then
            report FAIL hsts "max-age $max_age below $HSTS_MIN_MAX_AGE"
        else
            report PASS hsts "max-age $max_age"
        fi
    fi
fi

# 4. healthz: liveness must answer 200 with exactly the documented body.
if [ -z "$HEALTH_CODE" ]; then
    report FAIL healthz "request failed (curl exit $HEALTH_RC)"
elif [ "$HEALTH_CODE" != 200 ]; then
    report FAIL healthz "status $HEALTH_CODE (want 200)"
elif [ "$(cat "$WORK/health.body")" != '{"status":"ok"}' ]; then
    report FAIL healthz 'unexpected body'
else
    report PASS healthz '200 {"status":"ok"}'
fi

# 5. spa-headers: the SPA shell and the app's baseline response headers.
spa_reason=
if [ -z "$ROOT_CODE" ]; then
    spa_reason="GET / failed (curl exit $ROOT_RC)"
elif [ "$ROOT_CODE" != 200 ]; then
    spa_reason="status $ROOT_CODE (want 200)"
else
    case $(lower "$(header_value "$WORK/root.headers" Content-Type)") in
        text/html*) ;;
        '') spa_reason='missing Content-Type' ;;
        *) spa_reason='Content-Type is not text/html' ;;
    esac
    if [ -z "$spa_reason" ]; then
        if [ "$(lower "$(header_value "$WORK/root.headers" X-Content-Type-Options)" |
            sed 's/^[[:space:]]*//; s/[[:space:]]*$//')" != nosniff ]; then
            spa_reason='missing X-Content-Type-Options: nosniff'
        fi
    fi
    if [ -z "$spa_reason" ]; then
        # Browsers apply the last recognized token; requiring the last token to
        # be no-referrer is stricter (an unknown trailing token also fails).
        referrer=$(lower "$(header_all "$WORK/root.headers" Referrer-Policy | paste -sd, -)")
        referrer=${referrer##*,}
        if [ "$(printf '%s' "$referrer" | sed 's/^[[:space:]]*//; s/[[:space:]]*$//')" != no-referrer ]; then
            spa_reason='missing Referrer-Policy: no-referrer'
        fi
    fi
fi
if [ -n "$spa_reason" ]; then
    report FAIL spa-headers "$spa_reason"
else
    report PASS spa-headers 'html/nosniff/no-referrer'
fi

# 6. csp: the app sets none, so the deployed ingress must add one.
csp=$(header_value "$WORK/root.headers" Content-Security-Policy)
if [ -z "$csp" ] && [ "$MODE" != https ]; then
    report WARN csp 'loopback http; the ingress must add CSP with frame-ancestors'
elif [ -z "$csp" ]; then
    report FAIL csp 'missing Content-Security-Policy (ingress must add frame-ancestors)'
else
    # Match the directive name, not a substring: connect-src https://frame-ancestors.example
    # must not count as a frame-ancestors directive.
    csp_ok=false
    # frame-ancestors must restrict framing: only 'none' or 'self' sources.
    header_all "$WORK/root.headers" Content-Security-Policy | tr ';,' '\n\n' |
        awk '{ $0 = tolower($0) } NF && $1 == "frame-ancestors" {
                found = 1
                for (i = 2; i <= NF; i++) if ($i != "\047none\047" && $i != "\047self\047") bad = 1
            } END { exit !(found && !bad) }' &&
        csp_ok=true
    if [ "$csp_ok" = true ]; then
        report PASS csp 'frame-ancestors directive present'
    elif [ "$MODE" != https ]; then
        report WARN csp "CSP frame-ancestors missing or not 'none'/'self'"
    else
        report FAIL csp "CSP frame-ancestors missing or not 'none'/'self'"
    fi
fi

# 7. no-listing: Go's FileServer lists directories as a 200 page with <pre>.
fetch "$WORK/assets" GET "$BASE/assets/"
if [ -z "$HTTP_CODE" ]; then
    report FAIL no-listing "request failed (curl exit $HTTP_RC)"
elif [ "$HTTP_CODE" = 200 ] && grep -qF '<pre>' "$WORK/assets.body"; then
    report FAIL no-listing 'directory listing (200 with <pre>)'
else
    report PASS no-listing "no directory listing (status $HTTP_CODE)"
fi

# --- staff auth (shared by the staff-origin and staff-cookie checks) --------
fetch "$WORK/login" GET "$BASE/staff/auth/login"
# The BFF answers 503 staff_auth_unavailable until OIDC discovery loads; that
# is documented readiness, not a failure. Poll briefly, never in a burst.
tries=0
while [ "$HTTP_CODE" = 503 ] && [ "$tries" -lt 15 ]; do
    sleep 2
    tries=$((tries + 1))
    fetch "$WORK/login" GET "$BASE/staff/auth/login"
done
LOGIN_CODE=$HTTP_CODE

# 8. staff-origin: a cross-origin logout must be rejected.
if [ "$LOGIN_CODE" = 404 ] && [ "$MODE" != https ]; then
    report WARN staff-origin 'staff auth disabled'
elif [ "$LOGIN_CODE" = 404 ]; then
    # The pilot needs staff sign-in; a deployed origin without it is not ready.
    report FAIL staff-origin 'staff auth disabled (GET /staff/auth/login 404)'
else
    fetch "$WORK/logout" POST "$BASE/staff/auth/logout" \
        -H 'Origin: https://evil.invalid' --data ''
    if [ -z "$HTTP_CODE" ]; then
        report FAIL staff-origin "request failed (curl exit $HTTP_RC)"
    elif [ "$HTTP_CODE" = 403 ]; then
        report PASS staff-origin 'cross-origin logout rejected with 403'
    else
        report FAIL staff-origin "cross-origin logout returned $HTTP_CODE (want 403)"
    fi
fi

# 9. staff-cookie: the transaction cookie contract on the login redirect.
if [ "$LOGIN_CODE" = 404 ]; then
    : # staff auth disabled; the staff-origin WARN covers it.
else
    cookie_reason=
    case $LOGIN_CODE in
        302|303) ;;
        '') cookie_reason='login request failed' ;;
        *) cookie_reason="login returned $LOGIN_CODE (want 302 or 303)" ;;
    esac
    if [ -z "$cookie_reason" ] && [ "$MODE" = https ]; then
        case $(lower "$(header_value "$WORK/login.headers" Location)") in
            https://*) ;;
            *) cookie_reason='login Location is not https' ;;
        esac
    fi
    [ -z "$cookie_reason" ] && header_all "$WORK/login.headers" Set-Cookie >"$WORK/setcookie"
    # The callback needs both the transaction and the browser-binding cookie.
    for want in "$TX_COOKIE" "$BROWSER_COOKIE"; do
        cookie=
        if [ -z "$cookie_reason" ]; then
            while IFS= read -r line; do
                case $line in
                    "$want"=*)
                        cookie=$line
                        break ;;
                esac
            done <"$WORK/setcookie"
            [ -n "$cookie" ] || cookie_reason="missing $want cookie"
        fi
        if [ -z "$cookie_reason" ]; then
            # An empty transaction value is not a usable binding cookie.
            cookie_value=${cookie#*=}
            cookie_value=${cookie_value%%;*}
            [ -n "$(printf '%s' "$cookie_value" | tr -d '[:space:]')" ] ||
                cookie_reason="empty $want value"
        fi
        if [ -z "$cookie_reason" ]; then
            printf '%s\n' "$cookie" | tr ';' '\n' | sed 's/^[[:space:]]*//' >"$WORK/attrs"
            has_secure=false has_httponly=false has_path=false has_samesite=false has_domain=false
            bad_samesite=
            while IFS= read -r attr; do
                case $(lower "$attr") in
                    secure) has_secure=true ;;
                    httponly) has_httponly=true ;;
                    'path=/') has_path=true ;;
                    samesite=lax) has_samesite=true ;;
                    samesite*) bad_samesite=$(lower "$attr") ;;
                    domain=*) has_domain=true ;;
                esac
            done <"$WORK/attrs"
            missing=
            [ "$has_secure" = true ] || missing="$missing Secure"
            [ "$has_httponly" = true ] || missing="$missing HttpOnly"
            [ "$has_path" = true ] || missing="$missing Path=/"
            [ "$has_samesite" = true ] || missing="$missing SameSite=Lax"
            if [ "$has_domain" = true ]; then
                cookie_reason='cookie sets Domain'
            elif [ -n "${bad_samesite:-}" ]; then
                cookie_reason='cookie SameSite is not Lax'
            elif [ -n "$missing" ]; then
                cookie_reason="cookie missing$missing"
            fi
        fi
    done
    if [ -n "$cookie_reason" ]; then
        report FAIL staff-cookie "$cookie_reason"
    else
        report PASS staff-cookie "$TX_COOKIE and $BROWSER_COOKIE secure contract"
    fi
fi

# 10. db-port: the database must not be reachable from the public internet.
if [ -n "$OPT_DB_HOST" ]; then
    # nc exit codes cannot tell refused from DNS/other errors, so always use
    # curl: exit 7 is only a refused TCP connect. A connected PostgreSQL port
    # gives a non-HTTP reply, which must never read as PASS.
    # time_connect > 0 means the TCP handshake completed: the port is open
    # whatever happens next (a PostgreSQL or silent proxy never answers HTTP).
    db_connect=$(curl -q -sS --connect-timeout 3 --max-time 5 -o /dev/null \
        -w '%{time_connect}' "http://$OPT_DB_HOST:5432/" 2>/dev/null)
    db_rc=$?
    if [ -n "$db_connect" ] && [ "$(printf '%s' "$db_connect" | tr -d '0.')" != "" ]; then
        report FAIL db-port "$OPT_DB_HOST:5432 accepted a TCP connection"
    else
        case $db_rc in
            7) report PASS db-port "$OPT_DB_HOST:5432 closed (connection refused)" ;;
            28) report WARN db-port "$OPT_DB_HOST:5432 no TCP answer in 3s (filtered?); confirm the host firewall" ;;
            6) report FAIL db-port "unverified: $OPT_DB_HOST did not resolve" ;;
            *) report FAIL db-port "unverified (curl exit $db_rc)" ;;
        esac
    fi
fi

# The D8 rate limits are a separate, deliberate burst probe.
# D8 has three limits; each profile is a separate probe.
for burst_profile in v1 commit checkout; do
    printf 'NEXT: go run ./cmd/bursttest -base-url %s -allow-remote -profile %s   # D8 rate limits\n' "$BASE" "$burst_profile"
done

echo
if [ "$FAILED" -gt 0 ]; then
    printf 'RESULT: FAIL (%d failed)\n' "$FAILED"
    status=1
else
    printf 'RESULT: PASS\n'
    status=0
fi
TZ=Asia/Tokyo date '+%F %T %Z'
exit "$status"
