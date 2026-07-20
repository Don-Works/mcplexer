# since.awk — the `docker logs --since` window filter.
#
# The boundary is decoded in BEGIN from the already-normalised fixed-width
# string rather than being computed by a separate awk invocation, because awk
# treats a positional program string as a FILE operand once -f has been used:
# a "helper" call like `awk -f timelib.awk 'BEGIN{...}'` silently reads the
# program text as input and yields nothing, which reads downstream as "no
# --since was supplied" and replays the entire nine-day fixture on every pull.
#
# Comparison is on an (epoch-second, nanosecond) PAIR, never a single
# nanosecond count: epoch nanoseconds for 2026 are ~1.8e18 and an awk double
# stops being an exact integer past 2^53, so one number would quantise away
# exactly the boundary the collector's +1ns exclusive window depends on.
#
# The test is >= because Docker's --since is INCLUSIVE. Reproducing that
# faithfully is what makes the collector's cursorTS+1ns step do real work.
BEGIN {
    ssec = parse_stamp_sec(since_str)
    snano = parse_stamp_nano(since_str)
}
{
    sec = parse_stamp_sec($1)
    if (sec > ssec) { print; next }
    if (sec < ssec) next
    if (parse_stamp_nano($1) >= snano) print
}
