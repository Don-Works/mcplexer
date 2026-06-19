---
name: strip-background
description: Remove backgrounds from images using AI segmentation, edge flood-fill, or modal extraction
---

# Strip Background from Images

Remove backgrounds from images. Three modes: **AI segmentation** for photos (people, products, objects), **edge flood-fill** for UI screenshots with baked-in rounded corners, and **modal extraction** for screenshots of modals/dialogs with grey overlays.

## Usage

```
/strip-background path/to/image.png                         # Auto-detect mode
/strip-background path/to/directory/                         # All images in directory
/strip-background path/to/image.png --mode photo             # Force AI segmentation (rembg)
/strip-background path/to/image.png --mode ui                # Force edge flood-fill
/strip-background path/to/image.png --mode modal             # Extract modal from overlay screenshot
/strip-background path/to/image.png --output clean/          # Custom output directory
/strip-background path/to/image.png --model u2net            # Specific rembg model
/strip-background path/to/image.png --threshold 243          # Colour threshold for UI mode
/strip-background path/to/image.png --inset 8                # Inset pixels for modal mode
```

## Input

**Target:** `$ARGUMENTS`

If the argument is a file, process that file. If it's a directory, process all image files (`.png`, `.jpg`, `.jpeg`, `.webp`, `.bmp`, `.tiff`) in it. If no argument is given, ask the user what to process.

Supported flags:
- `--mode {photo|ui|modal}` — processing mode (default: auto-detect based on image content)
- `--output {dir}` — write results to a specific directory (default: same directory as source, with `_no_bg` suffix)
- `--model {name}` — rembg model for photo mode (default: `u2net`). Options: `u2net`, `u2netp` (lightweight), `u2net_human_seg` (optimised for people), `isnet-general-use`, `silueta`
- `--format {ext}` — output format: `png` (default, preserves transparency) or `webp`
- `--threshold {0-255}` — colour brightness threshold for UI mode (default: `243`). Pixels brighter than this connected to the image edges become transparent.
- `--inset {pixels}` — for modal mode, number of pixels to inset the crop inside the detected modal boundary (default: `8`). Eliminates grey overlay remnants at the modal edge.
- `--replace` — overwrite originals instead of creating `_no_bg` copies

## Mode Selection

**Auto-detect logic:** Read the image and apply these checks in order:

1. **Modal detection:** Use ImageMagick to threshold the image at 95% brightness and find the largest white rectangular region via `-trim`. If the detected region is significantly smaller than the full image (both width AND height are less than 80% of the original), the image likely contains a modal/dialog over a darker overlay. Use **Modal mode**.
2. **UI detection:** Check the corner regions (top-left, top-right, bottom-left, bottom-right 20x20px). If all four corners are near-white (average brightness > 240), use **UI mode**.
3. **Fallback:** Use **Photo mode**.

### Photo Mode (rembg)

Best for: photographs, product shots, people, logos on coloured backgrounds. Uses AI segmentation (U2-Net) to identify the foreground subject and remove everything else.

**Warning:** Do NOT use photo mode for UI screenshots, dashboards, or flat design images. rembg will produce artifacts, smudged edges, and damaged content because it cannot distinguish UI content from background when both are similar colours.

### UI Mode (edge flood-fill)

Best for: screenshots, dashboard images, cards with rounded corners, UI components on light backgrounds. Uses colour-threshold flood-fill from the image edges to make the surrounding background transparent while preserving all interior content.

This mode:
1. Finds all near-white pixels (all RGB channels above the threshold)
2. Flood-fills from the image edges inward, only following connected near-white pixels
3. Makes only those connected edge pixels transparent
4. Interior white content (text backgrounds, cards, input fields) is untouched

### Modal Mode (crop + flood-fill)

Best for: screenshots of modal dialogs, popups, or drawers that appear over a semi-transparent grey overlay. The grey overlay blocks edge flood-fill because the image edges are dark (nav bars, overlaid page content), not light.

This mode uses a two-step process:
1. **Find the modal boundary** using ImageMagick threshold detection to locate the white rectangular region
2. **Crop to the modal** with a configurable inset (default 8px) that cuts just inside the modal edge, completely eliminating the grey overlay
3. **Strip border-radius corners** using edge flood-fill on the cropped result (the corners of the modal have rounded edges on a now-white background)

**Why the inset matters:** The threshold detection finds the outermost white pixels of the modal. But the modal's border sits right against the grey overlay, so a pixel-perfect crop still includes a thin line of grey at the edges. The inset (default 8px at 2x retina = 4px at 1x) cuts a few pixels inside the modal boundary, ensuring a clean edge with zero grey remnants.

## Execution

### Step 1 — Check Prerequisites

For **photo mode**, verify `rembg` is installed:

```bash
rembg --help
```

If not available, install it. Prefer `pipx` for Homebrew Python environments:

```bash
pipx install "rembg[cpu]"
```

Or with a virtual environment:

```bash
python3 -m venv /tmp/rembg-env
source /tmp/rembg-env/bin/activate
pip install "rembg[cpu]"
```

For **UI mode** and **modal mode**, Python 3 with Pillow, NumPy, and SciPy is needed. Use a virtual environment if the system Python is externally managed:

```bash
python3 -m venv /tmp/strip-bg-env
source /tmp/strip-bg-env/bin/activate
pip install Pillow numpy scipy
```

**Modal mode** also requires ImageMagick (`magick` command):

```bash
magick --version
```

If not available: `brew install imagemagick`

### Step 2 — Identify Files

Collect all target image files. For directories, find all files matching supported extensions. Report what was found and which mode will be used:

```
Found 3 images to process:
  - hero.png (1.2 MB) → UI mode (light corners detected)
  - team-photo.jpg (890 KB) → Photo mode
  - modal-screenshot.png (520 KB) → Modal mode (white region 1148x1554 detected in 3456x1838 image)
```

### Step 3 — Remove Backgrounds

**Photo mode** — for each image, run:

```bash
rembg i -m {model} "{input_path}" "{output_path}"
```

**UI mode** — for each image, run this Python script:

```python
from PIL import Image
import numpy as np
from collections import deque
from scipy.ndimage import binary_dilation

def strip_ui_background(input_path, output_path, threshold=243, dilate=2):
    img = Image.open(input_path).convert("RGBA")
    data = np.array(img)
    h, w = data.shape[:2]
    brightness = (data[:,:,0].astype(float) + data[:,:,1].astype(float) + data[:,:,2].astype(float)) / 3
    is_light = brightness > threshold
    visited = np.zeros((h, w), dtype=bool)
    queue = deque()

    # Seed from all edge pixels that are light
    for x in range(w):
        if is_light[0, x]: queue.append((0, x)); visited[0, x] = True
        if is_light[h-1, x]: queue.append((h-1, x)); visited[h-1, x] = True
    for y in range(h):
        if is_light[y, 0]: queue.append((y, 0)); visited[y, 0] = True
        if is_light[y, w-1]: queue.append((y, w-1)); visited[y, w-1] = True

    # BFS flood fill
    mask = np.zeros((h, w), dtype=bool)
    while queue:
        cy, cx = queue.popleft()
        mask[cy, cx] = True
        for dy, dx in [(-1,0),(1,0),(0,-1),(0,1)]:
            ny, nx = cy+dy, cx+dx
            if 0 <= ny < h and 0 <= nx < w and not visited[ny, nx] and is_light[ny, nx]:
                visited[ny, nx] = True
                queue.append((ny, nx))

    # Dilate mask to eat border fringe and anti-aliased border-radius pixels
    if dilate > 0:
        dilated = binary_dilation(mask, iterations=dilate)
        fringe = dilated & ~mask & (brightness > 180)
        mask = mask | fringe

    data[mask, 3] = 0

    # Trim with 1px padding to preserve visible rounded corners
    result = Image.fromarray(data)
    bbox = result.getbbox()
    if bbox:
        pad = 1
        result = result.crop((max(0, bbox[0]-pad), max(0, bbox[1]-pad),
                              min(w, bbox[2]+pad), min(h, bbox[3]+pad)))
    result.save(output_path)
```

**Threshold calibration:** Before running, check corner pixel brightness to pick the right threshold. The threshold must be between the page background brightness and the card content brightness. Print corner values with:

```python
data = np.array(Image.open(input_path))
print("Corners:", [tuple(data[5,5,:3]), tuple(data[5,-5,:3]), tuple(data[-5,5,:3]), tuple(data[-5,-5,:3])])
```

**Dilate parameter:** Controls how many pixels of border fringe to eat. Default `2` works for most 1px CSS borders at 2x retina. Set `0` to disable (pure flood fill), increase for thicker borders.

**Modal mode** — two-step process for each image:

**Step 3a: Find the modal boundary and crop with inset**

```bash
# Find the white modal region using ImageMagick threshold + trim
BOUNDS=$(magick "{input_path}" -colorspace Gray -threshold 95% \
  -morphology Close Rectangle:5x5 -trim \
  -format '%[fx:page.x],%[fx:page.y],%[fx:w],%[fx:h]' info:)

# Parse bounds
IFS=',' read -r X Y W H <<< "$BOUNDS"

# Crop with inset (default 8px) to cut inside the modal edge
INSET=8
magick "{input_path}" \
  -crop $((W - INSET*2))x$((H - INSET*2))+$((X + INSET))+$((Y + INSET)) \
  +repage "{output_path}"
```

**Step 3b: Strip border-radius corners from the cropped modal**

Run the same edge flood-fill script (from UI mode above) on the cropped output with `threshold=240`.

```python
strip_ui_background("{output_path}", "{output_path}", threshold=240)
```

Then trim to the non-transparent bounding box:

```python
from PIL import Image

img = Image.open("{output_path}")
bbox = img.getbbox()
if bbox:
    img.crop(bbox).save("{output_path}")
```

**Output naming:**
- Single file: `{name}_no_bg.png` in the same directory (or `--output` directory)
- Directory: all outputs go to `{dir}_no_bg/` (or `--output` directory)
- If `--replace` is set, overwrite the original files directly

The output is always PNG (or WebP if `--format webp`) to preserve the transparent background.

### Step 4 — Verify Results

After processing, read each output file to visually verify the background was removed cleanly.

**For photo mode** — if the result has artifacts or damaged content, suggest:
- A different model (`u2net_human_seg` for people, `isnet-general-use` for objects)
- Switching to UI mode if the image is actually a screenshot

**For UI mode** — if too much or too little was removed, suggest adjusting the threshold:
- Lower threshold (e.g. `230`) removes more (catches darker background pixels)
- Higher threshold (e.g. `250`) removes less (only very bright pixels)

**For modal mode** — if grey remnants are still visible at the edges:
- Increase the inset (e.g. `--inset 12` or `--inset 16`) to crop further inside the modal
- If the modal content is being clipped, decrease the inset (e.g. `--inset 4`)
- If the wrong region was detected (e.g. a background element instead of the modal), the image may need manual cropping first

### Step 5 — Report

```
Background removed:
  - hero_no_bg.png (201 KB, was 275 KB, saved 27%) [UI mode]
  - team-photo_no_bg.png (450 KB, was 890 KB, saved 49%) [Photo mode]
  - modal_no_bg.png (115 KB, was 520 KB, saved 78%) [Modal mode, bounds: 1148x1554+1154+163, inset: 8]

Output directory: ./images_no_bg/
```

If any files failed, report them separately with the error.

## Prerequisites

| Dependency | Required for | Install |
|-----------|-------------|---------|
| Python 3.8+ | All modes | `brew install python` |
| Pillow + NumPy + SciPy | UI + Modal modes | `pip3 install Pillow numpy scipy` |
| rembg | Photo mode | `pipx install "rembg[cpu]"` |
| ImageMagick 7+ | Modal mode | `brew install imagemagick` |

The first rembg run downloads the U2-Net model (~170 MB). Subsequent runs are instant.

## Notes

- Output is always PNG or WebP to preserve transparency — JPEG does not support transparent backgrounds
- **UI mode** is the correct choice for dashboard screenshots, admin panels, and cards on light backgrounds. These have white/light backgrounds that rembg's AI cannot distinguish from content
- **Modal mode** is the correct choice for screenshots of modals, dialogs, popups, or drawers that appear over a semi-transparent grey overlay. These have dark edges (nav bars, dimmed page content) that block UI mode's edge flood-fill
- **Photo mode** uses AI segmentation that works on pixel data, handling border-radius, drop shadows, and complex edges on photographs
- For batch processing of many images, files are processed sequentially to avoid memory issues
- When updating images on a webpage after background removal, add `bg-white` to the image CSS class so the transparent areas render correctly on coloured page backgrounds
- The `u2netp` model is ~4x faster but slightly less accurate — good for previews or low-detail images
- The default inset of 8px assumes 2x retina screenshots (4px at 1x). For 1x screenshots, use `--inset 4`. For 3x, use `--inset 12`
