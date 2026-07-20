# events.awk — bounded `docker events` range filter.
#
# Fixture rows are "<fixed-width stamp>|<action>|<actor id>". The range bounds
# are decoded in BEGIN from already-normalised fixed-width strings; see the
# note in since.awk for why they are not computed by a helper awk call.
BEGIN {
    FS = "|"
    asec = parse_stamp_sec(from_str)
    bsec = parse_stamp_sec(to_str)
}
{
    sec = parse_stamp_sec($1)
    if (sec < asec || sec > bsec) next
    # TimeNano as Docker prints it. Assembled as a decimal STRING because the
    # value is ~1.8e18 and printing it through an awk double would round the
    # low digits away.
    printf "%d%09d|%s|%s\n", sec, parse_stamp_nano($1), $2, $3
}
