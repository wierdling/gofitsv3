# Gaia provider and cache

GoFitsV3's Gaia calibration is **SPCC-like**, not PixInsight code and not a
claim of PixInsight compatibility. It uses Gaia DR3 source photometry and XP
spectra to estimate reproducible channel gains. Artistic tint, layer opacity,
and creative color mapping remain explicit user choices; Gaia can fit an
overlay scalar but cannot choose an artistic tint.

## Network and privacy

Online mode sends only the requested sky footprint, release, magnitude and
quality query, plus the configured endpoint's normal HTTP metadata. It does not
upload image pixels or project files. The default public ESA endpoint uses TAP
for Gaia DR3 source discovery and ESA DataLink for the matched stars' sampled
XP spectra. Compatible JSON-adapter proxies with the expected `/sources` and
`/spectra` contract remain supported when configured explicitly.
`cacheOnly` mode performs no network requests and fails clearly on a missing
cell or spectrum.

## Cache lifecycle

The default SQLite cache is `<user cache>/gofitsv3/gaia-cache.sqlite`; portable
installations may provide an explicit path. SQLite WAL mode and a busy timeout
allow concurrent readers. Query cells are keyed by a canonical signature
(release, endpoint/path semantics, quality selector and magnitude limit) plus a
separate spatial-cell key. XP representation versions are keys on the XP rows
themselves. Sources are deduplicated by `(release, source_id)` and XP rows
by `(release, source_id, representation)`, so overlapping fields grow the
cache incrementally rather than duplicating records.

Schema migrations are transactional and carry a schema version in `metadata`.
An interrupted write leaves a cell incomplete; incomplete cells are retried
online and never treated as a cache hit. Deleting the cache is safe: it removes
only shared catalog data and causes the next online request to refetch. A saved
valid project stores transforms, diagnostics and provenance, so rendering that
project remains reproducible without the cache or network.

There is no automatic eviction or size enforcement policy. Monitor the SQLite
file and remove it or choose a new path when storage/privacy policy requires.

## Scientific limits and diagnostics

Positions are propagated from each source's reference epoch using proper
motion to the FITS observation epoch. Quality selectors, magnitude limit,
match radius and observation epoch are persisted with the calibration; minimum
star and rejection rules are algorithm behavior rather than persisted
thresholds. Supported passbands are the versioned assets reported by the
application (currently Gaia G/BP/RP and F435W/F606W/F814W); XP integration
requires wavelength coverage across the complete passband. Unsupported or
partially covered passbands fail explicitly rather than extrapolating.

During a fit, internal diagnostics include detected/matched/accepted/rejected
star counts, rejection reasons, residuals and robust scatter; these per-fit
details are not persisted or displayed by Compose. The persisted Compose state
retains the user-facing matched/accepted status message, source IDs,
catalog release, provider/passband provenance, Gaia query settings, and source
and settings fingerprints; full per-star residual detail is runtime diagnostic
data. These retained fields make a saved result auditable and participate in
staleness fingerprints.
