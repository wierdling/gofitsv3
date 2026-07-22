# GoFitsV3: A Beginner's Guide to Astronomical FITS Image Processing

Welcome to the **GoFitsV3 User Guide**! This document is written specifically for space enthusiasts and amateur astrophotographers who have never processed raw astronomical data before. By the end of this guide, you will know how to find raw Hubble Space Telescope (HST) files, load them, align them, combine multiple exposures to remove cosmic rays, merge different filters to create a colored image, and edit them into a beautiful, presentation-ready photograph.

---

## Table of Contents
1. [Understanding Astronomical Data (FITS)](#1-understanding-astronomical-data-fits)
2. [How to Find and Download Hubble Data](#2-how-to-find-and-download-hubble-data)
3. [The GoFitsV3 Interface Overview](#3-the-gofitsv3-interface-overview)
4. [Step-by-Step Workspace Guides](#4-step-by-step-workspace-guides)
   - [Mosaic Workspace (Aligning & Combining Exposures)](#mosaic-workspace-aligning--combining-exposures)
   - [Examine Workspace (Inspecting Raw Images & Metadata)](#examine-workspace-inspecting-raw-images--metadata)
   - [Compose Workspace (Building RGB Color Composites)](#compose-workspace-building-rgb-color-composites)
   - [Edit Workspace (Final Polish & Export)](#edit-workspace-final-polish--export)
5. [A Complete Processing Example (Walkthrough)](#5-a-complete-processing-example-walkthrough)
6. [Glossary of Terms](#6-glossary-of-terms)

---

## 1. Understanding Astronomical Data (FITS)

Unlike your smartphone, which saves photos as finished JPEGs or PNGs, space telescopes like Hubble capture scientific data and save them in a format called **FITS** (Flexible Image Transport System, usually ending in `.fits`, `.fit`, or `.fts`). 

### Why is FITS different from standard image formats?
1. **Raw Scientific Readings**: A FITS file stores the exact number of electrons detected by each pixel on the camera's sensor during an exposure.
2. **Extreme Dynamic Range**: Standard images use 8 bits (256 levels of brightness) or 16 bits per color channel. Astronomical sensors capture a much wider range of light—from faint nebulae to brilliant stars. When you first load a raw FITS file, it will usually look **completely black**. This is normal! The data must be "stretched" to make the faint details visible.
3. **No In-Camera Color**: Telescope sensors are monochrome (black and white). They use physical filters placed in front of the sensor to let only specific wavelengths of light through (e.g., red, green, blue, hydrogen-alpha, or infrared). To create a color image, you must download images taken through different filters and combine them into red, green, and blue channels yourself.
4. **Multiple "Extensions"**: A single FITS file contains one or more data layers (called HDUs, or Header Data Units). For Hubble files, these layers typically represent:
   * **SCI (Science)**: The actual image pixels containing the light values.
   * **DQ (Data Quality)**: A mask flagging bad pixels, hot pixels (sensor defects), or cosmic rays.
   * **ERR (Error)**: A noise map representing the uncertainty of each pixel's measurement, which is crucial for identifying cosmic rays.

Below is an example of a fully processed color image (Herbig-Haro 901 in the Carina Nebula) showing the high-fidelity detail you can achieve with GoFitsV3:

![HH 901 in the Carina Nebula, processed from raw HST data](images/Carina_HH901.png)

---

## 2. How to Find and Download Hubble Data

All Hubble Space Telescope data is public and stored in the **Mikulski Archive for Space Telescopes (MAST)**. You can search and download files directly from your web browser.

### Step-by-Step Data Retrieval
1. Go to the official MAST search portal: **[https://mast.stsci.edu/search/ui/#/hst](https://mast.stsci.edu/search/ui/#/hst)**
2. **Search for a Target**: In the search bar, type the name of a famous deep-sky object. Excellent beginner targets include:
   * `M16` (Eagle Nebula / Pillars of Creation)
   * `M51` (Whirlpool Galaxy)
   * `M42` (Orion Nebula)
   * `NGC 2237` (Rosette Nebula)
3. **Filter Your Results**:
   * **Instrument**: Select **WFC3** (Wide Field Camera 3) or **ACS** (Advanced Camera for Surveys). These are Hubble's main imaging instruments.
   * **Filters**: Look for observations taken in multiple filters. For a standard color image, you want three broadband filters (e.g., `F814W` for Red, `F555W` or `F606W` for Green, and `F438W` or `F390W` for Blue).
4. **Choose the Right Files (FLC vs. FLT)**:
   * When downloading data, you will see several file suffixes.
   * Always look for files ending in **`_flc.fits`** or **`_flt.fits`**.
   * **`_flc.fits`** files are corrected for charge-transfer efficiency (preferred).
   * **`_flt.fits`** files are calibrated flat-fielded exposures (use these if `_flc` is unavailable).
   * Avoid downloading `_drz.fits` or `_drc.fits` files initially, as these are already combined by automated pipelines. GoFitsV3 is designed to let you do this combining yourself for better custom control.
5. **Download**: Check the boxes next to the exposures you want and click the download button to save them to a folder on your computer.

---

## 3. The GoFitsV3 Interface Overview

GoFitsV3 is a desktop application built to make processing these raw files intuitive. The workflow is organized into four main tabs at the top of the window, representing the chronological steps of image processing:

```text
[1. Mosaic Tab] --> Align & Combine Exposures
      |
      v
[2. Examine Tab] --> Verify Single Frames & Headers
      |
      v
[3. Compose Tab] --> Merge Filters to RGB
      |
      v
[4. Edit Tab] --> Curves, Sharpen, Heal -> Export PNG/TIFF/WebP
```

1. **Mosaic**: Load raw, individual exposures from a single filter, align them together, and "drizzle" them into a single, clean image stack with cosmic rays removed.
2. **Examine**: Inspect single FITS files, view their raw histograms, explore FITS header metadata, and measure pixel coordinates.
3. **Compose**: Take your combined filter images (e.g., Red, Green, Blue filters), align them relative to each other, stretch them to reveal details, and merge them into a single color image.
4. **Edit**: Apply final touches to the color image, such as tone curves, sharpening, pixel healing, color speck cleaning, and export the file.

---

## 4. Step-by-Step Workspace Guides

---

### Mosaic Workspace (Aligning & Combining Exposures)

The Mosaic tab is where your processing begins. Telescopes take multiple short exposures of the same target to avoid overexposing bright stars and to allow the removal of random cosmic ray strikes. The Mosaic tab combines these exposures.

#### Step 1 - Loading Files

![Step 1 - Loading Files](images/screenshots/LoadFiles.png)

* **Add FITS**: Use this button to select and load individual `.fits` files manually.
* **Add Filter Batch**: A highly recommended tool for loading multiple files at once. Click this, select a folder containing downloaded Hubble files, and a scanner dialog will pop up.
  * **Filter Select (Recommended)**: Choose your filter (e.g. `F814W`, `F555W`) to isolate exposures of a single band. This is the primary way to load your image stacks.
  * **Advanced Selects**: Select options like *Proposal ID*, *Exposure Time*, *Product Type* (`flc` or `flt`), and *Observation Date*. These are strictly for advanced processing when combining multiple separate telescope visits or scientific proposals. Keep them at their default values for standard stacks.
  * Check the boxes next to the exposures you wish to load, and click **Load**.

#### Understanding the Reference Anchor (Drizzled Baseline)
When combining exposures or filters, they must register to the same spatial coordinate grid. In GoFitsV3, this is managed via the **Reference** setting:
* **With a Reference Anchor Set (Recommended for Multi-Filter Projects)**: Load a completed drizzled FITS file (for example, the finished mosaic of your Green channel) as the **Reference Input**. GoFitsV3 uses this file solely as a spatial coordinate (WCS) baseline grid. All loaded raw frames will align to this baseline, ensuring they match its dimensions, rotation, and pixel scale exactly. The pixel values of this reference frame are *not* merged into your combined output—only its geometry is used.
* **Without a Reference Anchor (Single-Filter Stacks)**: If no reference input is set, GoFitsV3 defaults to using the very first image in your list of loaded exposures as the spatial reference, and aligns all subsequent frames in the list to it.

#### Exposure Normalization
Exposures can have different exposure times or brightness units. Exposure Normalization scales all images to a common baseline (typically electrons per second) to prevent errors in cosmic-ray rejection.
* **Workflow**: After loading the exposures for each filter stack, you **must** open the normalization dialog via **`Mosaic -> Exposure Normalization...`** in the main menu, verify your settings, and click **Apply** to enforce the normalization before proceeding to alignment or drizzling.
* **Settings**:
  * `Off`: No scaling is applied to the raw counts.
  * `Auto` **(Recommended Default)**: Analyzes the exposure time (`EXPTIME`) and unit (`BUNIT`) headers in the FITS files and automatically scales them to a uniform rate when differences are detected.
  * `On`: Forcibly applies normalization based on exposure times.

#### Aligning the Exposures
Before combining the images, they must be aligned to sub-pixel accuracy.
* **Skip Alignment**: If the WCS pointing headers of the telescope are already perfect (common for simple dither patterns), you can skip alignment and drizzle directly. If you notice visual shifts between exposures when combined, you must run alignment.
* **Alignment Menu**: Open **`Mosaic -> Alignment Settings...`** to configure alignment.
* **Alignment Settings**:
  * **Alignment Model**:
    * `Translation-only`: Shifts images horizontally and vertically.
    * `Rotation & Scale` **(Recommended Default)**: Shifts, rotates, and scales. Best for typical dithered exposures.
    * `Full Affine`: Shifts, rotates, scales, and skews. Used for complex distortions.
  * **Search Radius (Arcsec)**: The angular radius searched around WCS coordinates to match stars. **Default: 5.0**.
  * **NumRefs**: The number of initial frames used as reference templates for alignment. **Default: 1**.
  * **Debug Alignment**: If checked, opens a diagnostic window showing matched star vectors. **Default: Off**.

![Alignment Settings](images/screenshots/AlignmentSettings.png)

* **Manual Star Alignment**: If automatic star matching fails:
  1. Click **Select Stars** to open Star Mode.
  2. Click on 3 to 10 clear, unsaturated stars in the main reference preview image.
  3. The software will detect their centroids and match them across all other frames.
  4. Click **Apply** to calculate the offsets.

#### Visually Inspecting Alignment
* **Blinker Tool**: Click **Blinker** in the upper-right menu. This opens a separate window cycling quickly through the aligned exposures. If aligned correctly, the stars should remain completely stationary. If they jump or drift, adjust your alignment settings or use manual star mode.

#### Sky Subtraction Settings (SkySub)
Background sky brightness can vary due to zodiacal light, scattered light, or small visit-to-visit calibration differences. Sky subtraction ensures all frames are matched to a common background level before cosmic-ray rejection.
* **AstroDrizzle Sky Subtraction**: Open via **`Mosaic -> Sky Subtraction Settings...`**.
* **Settings**:
  * **Enabled**: Check this box to turn background subtraction on. **Default: Off** (for simple stacks, but recommended when backgrounds vary).
  * **Sky Method**:
    * `localmin` **(Recommended Default)**: Calculates background levels based on a grid of local minimums. Recommended when images contain large nebulae or galaxy structures to prevent the sky calculation from treating gas as background.
    * `globalmin`: Calculates a single background level across the entire frame. Best for empty star fields.
    * `match`: Matches relative sky offsets between frames using their overlaps. This preserves the reference frame's overall level instead of forcing everything to zero.
    * `globalmin+match`: Combines global minimum subtraction with frame-to-frame matching.
    * `match+plane`: Like `match`, but also fits a relative gradient plane from overlap differences. This is useful for JWST fields with residual large-scale gradients or extended nebulosity, because it does not estimate the sky from object-filled regions directly.
  * **Sky Stat**:
    * `median` **(Recommended Default)**: Computes the median value of background pixels.
    * `mode` / `mean`: Statistical modes.
  * **Sky Width**: The width of the histogram bins used for mode estimation. **Default: 0.1**.
  * **Sky Clip**: The number of sigma-clipping iterations. **Default: 5**.
  * **Sky LSigma / USigma**: Lower and upper sigma limits for clipping outliers. **Default: 4.0**.

* **JWST Artifact Corrections**: Click **JWST Artifact Corrections...** inside the sky subtraction dialog for detector-level cleanup that runs before sky matching. These options are saved with the mosaic project and default to off for older projects.
  * **NIRCam Wisp Template**: Optional correction for detector-fixed wisps. It uses only local template FITS files; GoFitsV3 will not download references. Put templates in a folder with names like `nircam_wisp_nrcb4_f200w.fits`, then enter that folder in **Wisp template dir**. Auto-scale is recommended; fixed scale must be non-negative.
  * **NIRCam Amp Pedestal / Row Banding**: Optional detector-artifact corrections for calibrated NIRCam frames that still show amplifier steps or horizontal 1/f banding. These are separate from sky subtraction. Row banding supports optional binary FITS masks where nonzero pixels are excluded from row statistics; mask dimensions must exactly match each input.
  * **MIRI Artifact Mask**: Optional user-provided masks for calibrated MIRI images. A direct mask FITS can be supplied, or a mask folder can contain per-input files named `<input-stem>_miri_mask.fits` such as `jw12345_miri_mask.fits`. Nonzero mask pixels are excluded from sky matching, cosmic-ray modeling, and drizzle. For MIRI shower and snowball artifacts, the preferred fix is to rerun the current STScI JWST pipeline with its jump/shower handling before loading calibrated files into GoFitsV3.

* **Creating artifact / row-stat masks in GoFitsV3**: Use **`Mosaic -> Create Artifact Masks...`**.
  * **MIRI artifact exclusion** can be edited from a single MIRI input or from **Current Mosaic** after a drizzle result exists. Single-input masks can optionally be WCS-projected to other loaded MIRI frames. Current Mosaic masks are projected back into each overlapping MIRI detector frame before export. Export writes per-input files named `<input-stem>_miri_mask.fits`, enables the MIRI mask directory, and excludes nonzero mask pixels from sky matching, CR modeling, and drizzle.
  * **NIRCam row-stat exclusion** is edited only from a calibrated NIRCam input frame. Export writes `<input-stem>_rowmask.fits`, enables NIRCam row banding, and sets the row-mask directory. These masks protect the row-median estimator from bright stars, nebulous knots, or detector defects; they do **not** directly delete science pixels from the mosaic.
  * The default project-local output is `masks/`. Mask directories are saved relative to the mosaic project when possible, so moving the project with its `masks/` folder keeps the links working.
  * The export review shows target frames, dimensions, masked-pixel counts, detector-space previews, and an overwrite checkbox. Existing mask files are preserved unless **Overwrite existing mask** is checked.
  * Troubleshooting: if **Current Mosaic** is unavailable, build a drizzle result first and make sure the result has WCS metadata. If a mosaic-authored mask is marked stale, reopen/reproject it after rebuilding or changing alignment/drizzle geometry. A mask dimension error means the FITS mask does not match that specific calibrated input. MIRI showers/snowballs and severe ramp-level artifacts should usually be fixed by rerunning the current STScI JWST pipeline rather than by drawing masks over calibrated products.

![Sky Subtraction Settings](images/screenshots/SkysubSettings.png)

#### Drizzle & Combine (Create Mosaic)
Once aligned, you are ready to combine the images.
* **Drizzle Settings**: Open **`Mosaic -> Drizzle Settings...`**.
* **Settings**:
  * **Scale**: The resolution scale of the output image relative to the input. **Default: 1.0** (use `2.0` for high-resolution upscaling if you have many exposures).
  * **Pixfrac (Pixel Fraction)**: The size of the "drizzle drop" relative to the input pixel size. **Default: 0.8** (lower values like `0.6` yield sharper results but require more exposures to avoid gaps; use `1.0` if you have fewer than 4 exposures).
  * **Kernel**: The interpolation kernel.
    * `Square` **(Recommended Default)**: Standard reliable default.
    * `Point` / `Turbo` / `Gaussian` / `Tophat` / `Lanczos-2` / `Lanczos-3`: Advanced kernels. Lanczos kernels produce the sharpest details but require high exposure counts to avoid dark ringing artifacts around stars.
  * **Enable Cosmic-Ray Rejection**: Enables AstroDrizzle-style CR detection using the FITS `ERR` noise model. **Default: On**.

![Drizzle Settings](images/screenshots/DrizzleSettings.png)

1. Click **Create Mosaic** to begin the process.
2. **IMPORTANT SAVE STEP**: Once the drizzle combine finishes, you **must save the file** by clicking the **Save Drizzle FITS** button in the upper-right panel. This exports the 32-bit FITS file for this filter, which you will load into the Compose workspace later.
3. Click **Send to Examine** to review the result.

---

### Examine Workspace (Inspecting Raw Images & Metadata)

The Examine workspace acts as a scientific magnifying glass.

* **FITS Header Viewer**: The scrollable list on the right displays the raw metadata stored in the FITS file. Here you can find:
  * `INSTRUME` (e.g., `WFC3`)
  * `FILTER` (e.g., `F555W`)
  * `EXPTIME` (total exposure duration in seconds)
  * `DATE-OBS` (date of observation)
* **Measurement Tool**: Check **Measure offsets**:
  * Click point A and then point B on the image preview.
  * The interface will display the starting and ending pixel coordinates, the delta X and delta Y shifts, and the exact distance in pixels. This is helpful for measuring offsets manually.
* **Stretch Previews**: Choose between different stretches (`Linear`, `Log`, `Sqrt`, `Asinh`, `HistEq`) to check the structure of your image and its background noise levels.

Here is an example of Cassiopeia A (a supernova remnant) stretched using the MTF (Midtones Transfer Function) model to reveal the faint shockwaves and ejecta:

![Cassiopeia A supernova remnant, stretched using MTF](images/CasA_MTF.png)

---

### Compose Workspace (Building RGB Color Composites)

Once you have generated clean, drizzled mosaics for each of your filters (e.g., one FITS file for Red, one for Green, one for Blue), switch to the Compose tab to merge them into a single colored image.

Here is a color composite of the colliding Antennae Galaxies, showing dust lanes and star-forming regions:

![The colliding Antennae Galaxies color composite](images/AntennaeGalaxies_MT.png)

#### Mapping Channels
Assign your FITS files to the channel slots:
* **Blue Channel**: Load your shortest-wavelength filter (e.g., `F390W`, `F438W` or `OIII`).
* **Green Channel**: Load your middle-wavelength filter (e.g., `F555W`, `F606W` or `H-alpha`).
* **Red Channel**: Load your longest-wavelength filter (e.g., `F814W` or `SII`).
* *Tip: If you do not have three filters, you can load the same file into multiple channels, or use the "Copy Settings" menu item to help balance them.*

#### Aligning the Channels
Filters are photographed sequentially, so the telescope may have drifted between them.
* Open the **Compose Menu** and select **Align to Channel 2**. GoFitsV3 will detect stars across all three channels and apply a full affine transform to register the Red and Blue channels perfectly to the Green channel.

#### Stretching the Data
To make the image visible, you must configure the stretch parameters for each channel. GoFitsV3 offers several stretch modes:
1. **Linear**: Simple, but blows out bright details (like star cores) before faint nebulosity becomes visible.
2. **Logarithmic (Log)**: Excellent for compressing high dynamic range, but can wash out contrast.
3. **Asinh (Inverse Sine Hyperbolic)**: Highly recommended for color composites because it preserves color saturation in bright highlights (like stars) while boosting faint backgrounds.
4. **MTF (Midtones Transfer Function)**: A classic stretch driven by a single midtone parameter.
5. **GHS (Generalised Hyperbolic Stretch)**: The most powerful and flexible stretch available. GHS stretches the image selectively, allowing you to boost faint details without bloating stars:
   * **Strength**: How strongly the stretch is applied.
   * **Center (SP - Stretch Point)**: The pixel value you want to stretch around (usually set just above your background noise level).
   * **Symmetry (BP)**: Controls the width of the stretch region.

#### Blending an "Orange Layer" (4-Channel Composition)
If you have a fourth filter (for example, a narrow-band Hydrogen-Alpha `F656N` image that contains rich details), you can blend it in:
* Go to `Compose -> Add Orange Image...`.
* Load your fourth FITS file.
* This opens a control window allowing you to adjust the custom RGB color tint (defaults to a beautiful golden-orange) and opacity slider to screen-blend this detail layer over your composite.

#### Clean Operations
* **Cross-Channel Clean**: Click this under the `Compose` menu. It builds a protective star mask and cleans up single-channel cosmic rays or hot pixels that survived the drizzling stage.
* **Normalize Scale**: Adjusts the scale factor of the channels relative to Channel 2 to ensure a color-balanced starting point.

When finished, select `File -> Send Composite to Edit`.

---

### Edit Workspace (Final Polish & Export)

The Edit tab is where you perform traditional photography adjustments before saving your final file.

* **RGB Levels**: Use the sliders (min and max) to adjust the black and white clipping points for individual channels to balance the color background.
* **Tone Curves Widget**: Click and drag points on the curves graph to adjust contrast. You can edit the "All" curve (brightness) or adjust Red, Green, and Blue curves individually to remove color casts from dark background regions.
* **Sharpening**: Adjust the **Strength** (0 to 3) and **Radius** (0.5 to 10) sliders to apply an unsharp mask that enhances fine details in galaxies or nebula dust lanes.
* **Color Speck Clean (Clean Tab)**: Run this tool to scan the image for single-pixel "hot" specks (residual sensor defects that show up as isolated pure red, green, or blue pixels) and blend them away using neighboring pixels.
* **Heal Tool**: Check **Heal Tool** to fix blemishes manually:
  * Adjust the **Brush Size** slider.
  * Hold **Ctrl and Click** on a clean area of the background (the source).
  * **Click and drag** over a scratch, hot pixel, or bloated artifact (the destination) to stamp the clean background texture over it.
* **Exporting**: Click `File -> Export...` to save your image. Choose between:
  * **PNG** (8-bit or high-fidelity 16-bit)
  * **JPEG** (with quality controls)
  * **TIFF**
  * **WebP** (lossless compression)

---

## 5. A Complete Processing Example (Walkthrough)

Let's walk through the creation of a color image of the **Whirlpool Galaxy (M51)** using real Hubble data.

### Phase 1: Retrieve Data
1. Open the MAST portal. Search for `M51`.
2. Filter for instrument `WFC3` and product type `FLC`.
3. Locate exposures for filters:
   * `F814W` (Infrared/Red) - 4 exposures
   * `F555W` (Visible/Green) - 4 exposures
   * `F438W` (Blue) - 4 exposures
4. Download the files. Put them in a folder called `M51_Raw`.

### Phase 2: Drizzle Filter Stacks
1. Open GoFitsV3 and click the **Mosaic** tab.
2. Click **Add Filter Batch**, select the `M51_Raw` folder, select `F438W` and `flc`. Load them.
3. Select the first exposure as the **Reference**.
4. Click **Align Inputs**, select **Rotation & Scale** model, and click run.
5. Visually inspect alignment using the **Blinker** tool.
6. Click **Create Mosaic** with Scale `1.0`, Pixfrac `0.8`, Kernel `Square`, and Cosmic-Ray Rejection turned **On**.
7. Once completed, save the output FITS file as `M51_Blue_Drizzled.fits`.
8. Repeat this entire process for the `F555W` exposures (saving as `M51_Green_Drizzled.fits`) and `F814W` exposures (saving as `M51_Red_Drizzled.fits`).

### Phase 3: Build the Color Composite
1. Go to the **Compose** tab.
2. Load `M51_Blue_Drizzled.fits` into the Blue channel slot, `M51_Green_Drizzled.fits` into Green, and `M51_Red_Drizzled.fits` into Red.
3. Select `Compose -> Align to Channel 2` to register the three color channels together.
4. Set the Stretch mode on all channels to **Asinh**.
5. Adjust the sliders: set the Black levels just below the background peak on the histogram, and raise the stretch values until the galaxy's spiral arms are visible.
6. Select `Compose -> Cross-Channel Clean` to erase any remaining pixel artifacts.
7. Select `File -> Send Composite to Edit`.

### Phase 4: Final Adjustments & Save
1. In the **Edit** tab, open the **Curves** widget.
2. Select the **All** channel. Add an "S-Curve" (click to create a point in the dark tones and drag down, click a point in the highlights and drag up) to boost contrast.
3. Select the **Blue** channel and slightly lower the shadows if the background sky looks too blue.
4. Set **Sharpening Strength** to `0.8` and **Radius** to `1.2` to sharpen dust lanes.
5. Select the **Clean** tab, set the blob size to `2` and run **Color Speck Clean** to wipe out hot pixels.
6. Go to `File -> Export RGB Image`, choose **PNG 16-bit** format, name the file `M51_Whirlpool_Galaxy.png`, and click save.

*Congratulations, you have created a professional-grade space photograph!*

---

## 6. Glossary of Terms

* **Calibrated Files (FLT/FLC)**: Raw images that have been processed by the telescope's ground pipeline to correct for instrument bias, dark current, and flat-field illumination.
* **Cosmic Ray**: High-energy particles from space that strike the camera's sensor during exposure, leaving bright spots, lines, or hot pixels.
* **Dithering**: The practice of shifting the telescope's position slightly between exposures. This ensures that sensor defects (like dead pixels) don't land on the same parts of the target, allowing them to be easily filtered out.
* **Drizzle**: A digital image-processing technique that reconstructs high-resolution images from a set of dithered, low-resolution exposures.
* **FITS**: Flexible Image Transport System. The standard file format in astronomy for storing images, spectrum data, and extensive text headers.
* **GHS (Generalised Hyperbolic Stretch)**: A mathematical stretch algorithm that allows non-linear boosting of midtones or shadows without clipping bright highlights.
* **MAST**: Mikulski Archive for Space Telescopes. The central online repository for all NASA space telescope data, including Hubble, Kepler, and James Webb.
* **WCS (World Coordinate System)**: A coordinate mapping system stored inside FITS headers that links pixel positions (X, Y) directly to physical celestial coordinates (Right Ascension, Declination).
