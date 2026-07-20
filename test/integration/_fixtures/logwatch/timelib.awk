# timelib.awk — civil-date <-> epoch-second conversion for the logwatch
# fixtures and the loghost `docker` shim.
#
# WHY AWK AND NOT date(1): the generator emits ~13k lines spanning nine days
# and the shim re-filters that file on every pull. Shelling out to date(1)
# per line is unusable, and `date -d` is GNU-only (the generator runs on the
# developer's macOS host). These two functions are Howard Hinnant's
# days_from_civil / civil_from_days, which are exact integer arithmetic and
# identical on mawk, gawk and BSD awk.
#
# WHY SECONDS AND NANOS ARE KEPT SEPARATE: epoch nanoseconds for 2026 are
# ~1.8e18, well past the 2^53 boundary where an awk double stops representing
# integers exactly. Every arrival is therefore carried as an (sec, nano) pair
# and compared pairwise; nothing ever multiplies a second count by 1e9.

# dfc — days since 1970-01-01 for a proleptic Gregorian y-m-d.
function dfc(y, m, d,    era, yoe, doy, doe) {
    if (m <= 2) y -= 1
    era = int((y >= 0 ? y : y - 399) / 400)
    yoe = y - era * 400
    doy = int((153 * (m + (m > 2 ? -3 : 9)) + 2) / 5) + d - 1
    doe = yoe * 365 + int(yoe / 4) - int(yoe / 100) + doy
    return era * 146097 + doe - 719468
}

# cfd — civil y-m-d for a day count since 1970-01-01, returned "y m d".
function cfd(z,    era, doe, yoe, y, doy, mp, d, m) {
    z += 719468
    era = int((z >= 0 ? z : z - 146096) / 146097)
    doe = z - era * 146097
    yoe = int((doe - int(doe / 1460) + int(doe / 36524) - int(doe / 146096)) / 365)
    y = yoe + era * 400
    doy = doe - (365 * yoe + int(yoe / 4) - int(yoe / 100))
    mp = int((5 * doy + 2) / 153)
    d = doy - int((153 * mp + 2) / 5) + 1
    m = mp + (mp < 10 ? 3 : -9)
    if (m <= 2) y += 1
    return y " " m " " d
}

# stamp — fixed-width RFC3339Nano UTC for an (epoch-second, nano) pair.
#
# Fixed width on purpose. Real `docker logs --timestamps` emits nine
# fractional digits, Go's time.RFC3339Nano PARSER accepts any number of them,
# and a fixed width makes `sort -k1,1` on the rendered line chronological —
# which is how compose_fixture merges independently generated shapes.
function stamp(sec, nano,    days, rem, ymd, parts, hh, mi, ss) {
    days = int(sec / 86400)
    rem = sec - days * 86400
    if (rem < 0) { rem += 86400; days -= 1 }
    split(cfd(days), parts, " ")
    hh = int(rem / 3600)
    mi = int((rem % 3600) / 60)
    ss = rem % 60
    return sprintf("%04d-%02d-%02dT%02d:%02d:%02d.%09dZ",
                   parts[1], parts[2], parts[3], hh, mi, ss, nano)
}

# parse_stamp_sec — epoch seconds from a fixed-width stamp() string.
function parse_stamp_sec(s,    y, m, d, hh, mi, ss) {
    y  = substr(s, 1, 4) + 0
    m  = substr(s, 6, 2) + 0
    d  = substr(s, 9, 2) + 0
    hh = substr(s, 12, 2) + 0
    mi = substr(s, 15, 2) + 0
    ss = substr(s, 18, 2) + 0
    return dfc(y, m, d) * 86400 + hh * 3600 + mi * 60 + ss
}

# parse_stamp_nano — nanosecond field from a fixed-width stamp() string.
function parse_stamp_nano(s) {
    return substr(s, 21, 9) + 0
}

# lcg — deterministic Park-Miller minimal-standard PRNG, seeded per shape so a
# regenerated fixture is byte-identical: an integration assertion that moves
# between runs is worse than no assertion.
#
# The multiplier is 16807 and not the more familiar glibc 1103515245 for a
# precision reason, not a statistical one: awk numbers are doubles, and
# 1103515245 * 2^31 is ~2.4e18, far past the 2^53 boundary where integer
# arithmetic stops being exact. The product would silently lose its low bits —
# which are exactly the bits every `% n` below reads. 16807 * 2147483646 is
# ~3.6e13 and exact, so the sequence is the same on every awk.
function lcg(state,    s) {
    s = state % 2147483647
    if (s <= 0) s += 2147483646
    return (16807 * s) % 2147483647
}
