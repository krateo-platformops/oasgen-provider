#!/usr/bin/env bash
# sweep.sh <version> -- are all five release artifacts published?
#
# TWO INSTRUMENTS, because the five rows are two different path shapes and one probe
# cannot see both:
#   charts/*  -> `helm show chart oci://...`, which asks through the OCI client that
#                actually pulls, so it sees what `helm install` would see. A raw registry
#                manifest probe returns 404 for chart paths at EVERY version.
#   images    -> a registry manifest probe, which also yields the platform list.
#
# CONTROLS ARE PER SHAPE and the output is gated on all four. An image-path control that
# passes says nothing about a chart row -- that mistake read a fully published release as
# 2/5 twice in one day.
set -u
V="${1:?usage: sweep.sh <version>}"
PREV="${2:-}"                      # the outgoing version, as the positive control
IMPOSSIBLE="9.99.9"
REG="ghcr.io/krateo-platformops"
CHARTS=(charts/oasgen-provider charts/oasgen-provider-crds charts/controller-render-service)
# Charts that ship NO image: 4 files, 0 templates containing `image:`, 0 Deployments. They have no
# appVersion to set, so requiring one is a FALSE POSITIVE -- which the first run of the link check
# below duly produced. An absence with a legitimate cause needs its own branch, not a louder alarm.
IMAGELESS=(charts/oasgen-provider-crds)
IMAGES=(oasgen-provider rest-dynamic-controller)

chart_has() { helm show chart "oci://$REG/$1" --version "$2" 2>&1 | grep -qE "^version: $2$" && echo 1 || echo 0; }
tok() { curl -s "https://ghcr.io/token?service=ghcr.io&scope=repository:krateo-platformops/$1:pull" \
          | python3 -I -c "import json,sys; print(json.load(sys.stdin).get('token',''))"; }
img_has() { local t; t=$(tok "$1"); [ "$(curl -s -o /dev/null -w "%{http_code}" -H "Authorization: Bearer $t" \
          -H "Accept: application/vnd.oci.image.index.v1+json,application/vnd.docker.distribution.manifest.list.v2+json" \
          "https://ghcr.io/v2/krateo-platformops/$1/manifests/$2")" = "200" ] && echo 1 || echo 0; }
img_plat() { local t; t=$(tok "$1"); curl -s -H "Authorization: Bearer $t" -H "Accept: application/vnd.oci.image.index.v1+json" \
          "https://ghcr.io/v2/krateo-platformops/$1/manifests/$2" | python3 -I -c "
import json,sys
try: d=json.load(sys.stdin)
except Exception: print('UNREADABLE'); raise SystemExit
ms=d.get('manifests',[])
ps=sorted({m['platform']['os']+'/'+m['platform']['architecture'] for m in ms if m.get('platform',{}).get('architecture') not in (None,'unknown')})
print((','.join(ps) or 'NONE')+'  attestations='+str(sum(1 for m in ms if m.get('platform',{}).get('architecture')=='unknown')))
"; }

if [ -z "$PREV" ]; then echo "FATAL: pass the OUTGOING version as arg 2; it is the positive control"; exit 2; fi

echo "CONTROLS, one pair per path shape:"
CP=$(chart_has "${CHARTS[0]}" "$PREV"); CN=$(chart_has "${CHARTS[0]}" "$IMPOSSIBLE")
IP=$(img_has  "${IMAGES[0]}" "$PREV"); IN=$(img_has  "${IMAGES[0]}" "$IMPOSSIBLE")
printf "  CHART path  %s@%s=%s (want 1)   @%s=%s (want 0)\n" "${CHARTS[0]}" "$PREV" "$CP" "$IMPOSSIBLE" "$CN"
printf "  IMAGE path  %s@%s=%s (want 1)   @%s=%s (want 0)\n" "${IMAGES[0]}" "$PREV" "$IP" "$IMPOSSIBLE" "$IN"
if [ "$CP$CN$IP$IN" != "1010" ]; then echo "CONTROLS FAILED -- this measures nothing. Do not read the rows below."; exit 2; fi
echo "BOTH CONTROL PAIRS HOLD"

n=0
for c in "${CHARTS[@]}"; do h=$(chart_has "$c" "$V"); n=$((n+h)); printf "  %-38s %s\n" "$c" "$h"; done
for i in "${IMAGES[@]}"; do h=$(img_has  "$i" "$V"); n=$((n+h)); printf "  %-38s %s\n" "$i" "$h"; done
echo "SWEEP $V: $n/5"

# THE LINK BETWEEN THE TWO SHAPES, which checking each alone cannot see. Both templates resolve
# their image through `defaultTag .Chart.AppVersion` with `image.tag: ""` shipped, so appVersion is
# what a deployed pod actually pulls. A chart published at $V whose appVersion is the PREVIOUS
# release passes every row above and then deploys the old image.
#
# Raised by the installer session, from the other direction: their chart probes verify that a chart
# resolves and say nothing about the image it will pull.
echo "CHART -> IMAGE LINK:"
link_ok=1
for c in "${CHARTS[@]}"; do
  av=$(helm show chart "oci://$REG/$c" --version "$V" 2>/dev/null | grep -E "^appVersion:" | awk '{print $2}')
  imageless=0; for x in "${IMAGELESS[@]}"; do [ "$x" = "$c" ] && imageless=1; done
  if [ "$imageless" = "1" ]; then
    # A chart with no image must have NO appVersion. One appearing means it grew a workload, and
    # this list is stale -- which is a finding, not a pass.
    if [ -z "$av" ]; then printf "  %-38s no image, no appVersion (expected)\n" "$c"
    else printf "  %-38s appVersion=%s but this chart is listed as IMAGELESS -- the list is stale\n" "$c" "$av"; link_ok=0; fi
  elif [ "$av" = "$V" ]; then printf "  %-38s appVersion=%s\n" "$c" "$av"
  else printf "  %-38s appVersion=%s  MISMATCH, want %s\n" "$c" "${av:-<none>}" "$V"; link_ok=0; fi
done
# The control: the OUTGOING chart must link to the OUTGOING version, or this check cannot fail.
pav=$(helm show chart "oci://$REG/${CHARTS[0]}" --version "$PREV" 2>/dev/null | grep -E "^appVersion:" | awk '{print $2}')
printf "  CONTROL %-30s appVersion=%s (want %s)\n" "${CHARTS[0]}@$PREV" "${pav:-<none>}" "$PREV"
[ "$pav" = "$PREV" ] || { echo "  LINK CONTROL FAILED -- the appVersion rows above measure nothing"; link_ok=2; }
if [ "$n" = "5" ] && [ "$link_ok" = "1" ]; then
  echo "MULTI-ARCH (image paths only -- the chart instrument does not yield platforms):"
  for i in "${IMAGES[@]}"; do printf "  %-26s %s\n" "$i" "$(img_plat "$i" "$V")"; done
  printf "  %-26s %s\n" "CONTROL @$PREV" "$(img_plat "${IMAGES[0]}" "$PREV")"
  printf "  %-26s %s\n" "CONTROL @$IMPOSSIBLE" "$(img_plat "${IMAGES[0]}" "$IMPOSSIBLE")"
  echo "VERDICT: GREEN 5/5"
else
  if [ "$link_ok" = "2" ]; then echo "VERDICT: LINK CONTROL FAILED -- measures nothing"
  elif [ "$link_ok" = "0" ]; then echo "VERDICT: a chart points at the WRONG image version"
  else echo "VERDICT: INCOMPLETE $n/5"; fi
fi
