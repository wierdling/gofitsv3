# Starless Pipeline Settings

This document explains the Compose starless narrowband settings and how they affect detection, masking, inpainting, and final recombination.

The starless pipeline is optional and only runs in Compose when `Enable starless processing` is turned on.

## Pipeline Overview

The current pipeline works in these stages:

1. Build a shared star-detection image, or detect stars separately per channel and merge accepted seeds.
2. Build validity masks so no-data or clipped-black regions are excluded.
3. Estimate local background and noise.
4. Detect compact star seed points.
5. Grow radial star masks from those seeds.
6. Feather the hard mask into a soft alpha mask.
7. Inpaint stars out of each channel.
8. Build per-channel star layers using:

```text
starLayer = max(original - starless, 0)
```

9. Recombine starless RGB and star layers using the final alpha mask and validity mask. Alpha is applied during recombination.

## Settings

### Enabled

Turns the Compose starless pipeline on or off.

- `Off`: normal Compose preview/export path.
- `On`: Compose preview/export runs through the starless pipeline first.

Recommended default:

- `Off`

Use this when:

- you want to compare normal RGB vs starless-assisted RGB
- you are tuning the starless settings and want an easy before/after check

### Detection Mode

Controls how the aligned channels are combined into one shared detection image.

Options:

- `median`
- `min`
- `max`

What they do:

- `median`: balanced default; works well when stars are present in all channels.
- `min`: conservative; helps suppress features that appear strongly in only one messy channel.
- `max`: aggressive; can recover faint stars visible strongly in only one channel, but can also pick up more artifacts.

Recommended default:

- `median`

Tuning guidance:

- If nebula knots or one noisy channel are becoming candidate stars, try `min`.
- If real stars are being missed because they are much stronger in one channel, try `max`.

### Detection Merge Mode

Controls whether star seeds are detected once from the shared detection image or independently per channel.

Options:

- `shared`: current conservative behavior; detect seeds from one shared detection image.
- `per-channel-merged`: detect accepted seeds separately in each channel, merge nearby seeds, and use the largest merged radius for one shared mask.

Recommended default:

- `shared`

When to change it:

- Use `per-channel-merged` when stars are large, saturated, or visible mostly in only one narrowband channel.
- Keep `shared` when channels have similar star profiles and you want fewer chances for one noisy channel to contribute detections.

### Threshold Sigma

Sets the basic detection threshold above local background:

```text
peak > background + ThresholdSigma * localSigma
```

Higher values:

- fewer detected seeds
- fewer false positives
- more likely to miss faint stars

Lower values:

- more detected seeds
- more aggressive star extraction
- more likely to pick up nebula knots or texture

Recommended default:

- `4.0`

Typical tuning range:

- `3.0` to `6.0`

When to change it:

- Increase if `candidate_mask` is lighting up diffuse structure.
- Decrease if obvious stars are missing from `accepted_seeds`.

### Background Tile Size

Controls the tile size for local median/MAD background and noise estimation.

Smaller values:

- background follows local changes more tightly
- can overreact inside large stars or strong structure

Larger values:

- smoother background/noise estimate
- may miss small local seams or abrupt local changes

Recommended default:

- `32`

Typical tuning range:

- `16` to `64`

When to change it:

- Decrease if local seams or abrupt local gradients are slipping through.
- Increase if large stars or broad halos are confusing the local background estimate.

### No-Data Floor

When `Use No-Data Floor` is enabled, any pixel at or below this value is treated as invalid for starless detection and mask generation. When it is disabled, validity only requires finite pixels, so negative calibrated backgrounds can remain valid.

Invalid pixels do not participate in:

- normalization
- background estimation
- sigma estimation
- seed detection
- inpainting sample selection
- star recombination

Recommended default:

- `Use No-Data Floor = false`
- `No-Data Floor = 0.0`

Typical tuning range:

- `0.0` to a small positive value, depending on your preprocessing

When to change it:

- Increase if clipped-black seams, low-coverage bands, or edge voids are still leaking into detection.
- Enable `Use No-Data Floor` only when the data really uses a floor value as clipped/no-coverage/no-data.
- Keep it low if true sky background is legitimately near zero and you do not want to exclude it.

Important note:

- This is a detection-validity control, not a display stretch control.

### Seed Min Prominence

Sets the minimum local prominence required for a seed candidate to survive.

Prominence is the peak value compared with the surrounding annulus/ring. This helps reject nebula texture and filament edges that are bright but not compact point sources.

Higher values:

- fewer accepted seeds
- stronger rejection of diffuse structure
- more likely to miss faint stars

Lower values:

- more accepted seeds
- more sensitive to weak stars
- more risk of accepting nebula knots

Recommended default:

- `0.035`

Typical tuning range:

- `0.02` to `0.08`

When to change it:

- Increase if `accepted_seeds` lands on nebulosity.
- Decrease if obvious compact stars are making it into `candidate_mask` but not into `accepted_seeds`.

### Suppression Radius

Controls non-maximum suppression between seed points.

Nearby local maxima within this radius collapse into one accepted seed so one star does not generate many masks.

Higher values:

- fewer duplicate seeds around large stars
- more conservative seeding in crowded fields
- may merge close double stars

Lower values:

- more seeds in crowded regions
- more risk of multiple seeds on one bright halo

Recommended default:

- `10`

Typical tuning range:

- `8` to `12`

When to change it:

- Increase if one star produces multiple seed points.
- Decrease if close neighboring stars are collapsing into one seed.

### Mask Base Radius

The minimum radial mask size painted around each accepted star seed.

This is the floor before any brightness- or footprint-based radius growth.

Higher values:

- broader masks around ordinary stars
- safer removal of halos
- more risk of removing nearby nonstellar detail

Lower values:

- tighter masks
- less collateral masking
- more chance of residual star halos

Recommended default:

- `1`

Typical tuning range:

- `1` to `4`

When to change it:

- Increase if small stars leave visible residuals.
- Decrease if masks are too bloated around ordinary stars.

### Mask Max Radius

The maximum allowed radial mask size for any star seed.

Higher values:

- allows large saturated stars to get broader masks
- can be safer for broad halos
- can become too aggressive if seed detection is still too loose

Lower values:

- tighter cap on mask growth
- safer against runaway masks
- may under-mask large saturated stars

Recommended default:

- `8`

Typical tuning range:

- `6` to `24`

When to change it:

- Increase if large stars are not fully masked.
- Decrease if a false seed creates an overly large circular mask.

### Feather Radius

Controls soft-edge feathering of the hard mask into the final alpha mask.

Higher values:

- softer transitions
- smoother recombination
- broader star-layer edge

Lower values:

- tighter transitions
- crisper star separation
- more chance of abrupt edges

Recommended default:

- `1`

Typical tuning range:

- `0` to `4`

When to change it:

- Increase if the star layer looks harsh or cut out.
- Decrease if stars look too soft or too spread out in the recombined image.

### Inpaint Radius

Controls how far the inpainting stage searches for valid source pixels when filling masked star regions.

Higher values:

- more source pixels available
- better recovery around large masked stars
- more risk of blending across structure

Lower values:

- more local fills
- less blending across distant structure
- more risk of poor fill in larger masks

Recommended default:

- `8`

Typical tuning range:

- `4` to `16`

When to change it:

- Increase if removed stars leave obvious holes or poor fills.
- Decrease if fills look overly smeared across local structure.

### Star Brightness

Scales how strongly the extracted star layer is added back during recombination.

Higher values:

- brighter stars in final RGB

Lower values:

- dimmer stars in final RGB

Recommended default:

- `1.0`

Typical tuning range:

- `0.7` to `1.3`

When to change it:

- Lower if stars dominate the final RGB.
- Raise if stars feel too suppressed after starless processing.

### Star Saturation

Controls how much original star color is preserved during recombination.

Behavior:

- `0`: neutral/gray stars
- `1`: preserve original star color balance

Intermediate values partially desaturate stars.

Recommended default:

- `1.0`

Typical tuning range:

- `0.3` to `1.0`

When to change it:

- Lower if stars are producing distracting color noise or chromatic artifacts.
- Keep high if you want the original star color preserved.

### Diagnostic Images

Controls whether the starless debug views are shown on Apply/Save.

When enabled, the app can show intermediate pipeline outputs such as:

- detection image
- raw candidate mask
- accepted seeds
- rejected seeds
- final hard mask
- final alpha mask
- starless channel
- star layer

Use this whenever tuning the detector.

Recommended default:

- `Off`

## How To Tune

The safest order is:

1. Clean up seed detection.
2. Then tune radial mask size.
3. Then tune inpainting.
4. Then tune final star brightness/saturation.

### Step 1: Check detection

Look at:

- `compose_detection.png`
- `compose_candidate_mask.png`
- `compose_accepted_seeds.png`
- `compose_rejected_seeds.png`

What to look for:

- If `candidate_mask` already covers nebula, increase `Threshold Sigma` or `No-Data Floor`.
- If `accepted_seeds` lands on nebula knots, increase `Seed Min Prominence` or `Suppression Radius`.
- If obvious stars are absent from `accepted_seeds`, lower `Threshold Sigma` or `Seed Min Prominence`.

### Step 2: Check mask growth

Look at:

- `compose_hard_mask.png`
- `compose_alpha_mask.png`

What to look for:

- If large stars are clipped, increase `Mask Max Radius`.
- If ordinary stars are too fat, lower `Mask Base Radius`.
- If mask edges feel harsh, increase `Feather Radius`.

### Step 3: Check starless fill

Look at:

- `compose_starless_ch1.png`
- `compose_starless_ch2.png`
- `compose_starless_ch3.png`

What to look for:

- If removed stars leave obvious holes, increase `Inpaint Radius`.
- If the fill looks smeared or invasive, decrease `Inpaint Radius`.

### Step 4: Check star layer and final look

Look at:

- `compose_stars_ch1.png`
- `compose_stars_ch2.png`
- `compose_stars_ch3.png`

What to look for:

- If the star layer still contains nonstellar structure, go back to seed detection first.
- If stars are too strong in the final RGB, reduce `Star Brightness`.
- If star colors are distracting, lower `Star Saturation`.

## Quick Tuning Recipes

### Nebula is getting classified as stars

Try:

- raise `Threshold Sigma`
- raise `Seed Min Prominence`
- switch `Detection Mode` to `min`
- slightly raise `No-Data Floor` if seams/voids are involved

### Big stars are under-masked

Try:

- raise `Mask Max Radius`
- slightly raise `Mask Base Radius`
- keep `Threshold Sigma` reasonable so the seed is not lost

### One star creates multiple nearby seeds

Try:

- raise `Suppression Radius`

### Close double stars collapse into one

Try:

- lower `Suppression Radius`

### Invalid edge bands or clipped-black seams leak into the star layer

Try:

- raise `No-Data Floor`
- verify `compose_candidate_mask.png` and `compose_accepted_seeds.png` stay dark there

## Practical Starting Point

For a conservative first pass:

- `Detection Mode = median`
- `Detection Merge Mode = shared`
- `Threshold Sigma = 4.5`
- `Background Tile Size = 32`
- `Use No-Data Floor = false`
- `No-Data Floor = 0.0`
- `Seed Min Prominence = 0.04`
- `Suppression Radius = 10`
- `Mask Base Radius = 1`
- `Mask Max Radius = 10`
- `Feather Radius = 1`
- `Inpaint Radius = 8`
- `Star Brightness = 1.0`
- `Star Saturation = 1.0`

For stronger nebula rejection:

- raise `Threshold Sigma` to `5.0`
- raise `Seed Min Prominence` to `0.05`
- try `Detection Mode = min`
