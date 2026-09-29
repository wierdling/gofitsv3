"""Developer FITS verification and full-resolution source-stamp review.
Usage: python cmd/starmap/review.py science.fits working/filter_starmap.fits output_dir
Requires Astropy, NumPy and Matplotlib; not a desktop runtime dependency.
"""
import argparse
import json
from pathlib import Path
import numpy as np
from astropy.io import fits
import matplotlib
matplotlib.use('Agg')
import matplotlib.pyplot as plt
from matplotlib.patches import Circle

parser=argparse.ArgumentParser()
parser.add_argument('science',type=Path)
parser.add_argument('map',type=Path)
parser.add_argument('output',type=Path)
args=parser.parse_args()
args.output.mkdir(parents=True,exist_ok=True)
with fits.open(args.science,memmap=False) as hdus:
    science=hdus[0].data.copy()
    source_header=hdus[0].header.copy()
with fits.open(args.map,memmap=False) as hdus:
    hdus.verify('exception')
    mask=hdus[0].data
    catalog=hdus['STARS'].data.copy()
    flags=hdus['FLAGS'].data
    assert mask.shape==science.shape
    assert np.all(np.isfinite(mask)) and mask.min()>=0 and mask.max()<=1
    assert np.all(mask[~np.isfinite(science)]==0)
    assert np.all((flags[~np.isfinite(science)] & 1)!=0)
    for key in ('CTYPE1','CTYPE2','CRPIX1','CRPIX2','CRVAL1','CRVAL2','CD1_1','CD1_2','CD2_1','CD2_2'):
        assert hdus[0].header[key]==source_header[key],key
    mode=hdus[0].header['SMAPMODE']
accepted=(catalog['OVERRIDE']=='accept')|((catalog['STATUS']=='accepted')&(catalog['OVERRIDE']!='reject'))
summary=dict(mode=mode,candidates=len(catalog),accepted=int(accepted.sum()),uncertain=int((~accepted).sum()),saturated_accepted=int((accepted&catalog['SATURATED']).sum()),strict_fits=True)
(args.output/'summary.json').write_text(json.dumps(summary,indent=2))
fig,ax=plt.subplots(figsize=(11,11))
valid=science[np.isfinite(science)]
scale=max(float(np.percentile(valid,90))/3,1e-10)
ax.imshow(np.arcsinh(np.nan_to_num(science,nan=0)/scale),origin='lower',cmap='gray',vmin=0,vmax=4)
for s in catalog[accepted]:
    ax.add_patch(Circle((s['X']-1,s['Y']-1),max(12,s['RADIUS']),fill=False,color='lime',lw=.65))
ax.set_title(f'{args.science.stem}: {summary["accepted"]} selected ({mode})')
fig.tight_layout();fig.savefig(args.output/'overview.png',dpi=120);plt.close(fig)
# Inspect every accepted source, faintest first; do not select only easy stars.
selected=catalog[accepted];selected=selected[np.argsort(selected['AMPLITUDE'])]
for start in range(0,len(selected),30):
    fig,axes=plt.subplots(5,6,figsize=(14,12))
    for ax,s in zip(axes.flat,selected[start:start+30]):
        x,y=int(round(s['X']-1)),int(round(s['Y']-1));r=20
        cut=science[max(0,y-r):y+r+1,max(0,x-r):x+r+1]
        v=np.nanpercentile(cut,[10,99.5]);stretch=max(float(v[1]-v[0])/10,1e-10)
        ax.imshow(np.arcsinh(np.maximum(0,cut-v[0])/stretch),origin='lower',cmap='gray',vmin=0,vmax=np.arcsinh(10))
        ax.add_patch(Circle((s['X']-1-max(0,x-r),s['Y']-1-max(0,y-r)),s['RADIUS'],fill=False,color='lime',lw=.8))
        ax.set_title(f'{s["ID"]}: {x},{y}\nres {s["RESIDUAL"]:.2f} votes {s["CONFIRMED"]}',fontsize=8)
        ax.set_xticks([]);ax.set_yticks([])
    for ax in list(axes.flat)[len(selected[start:start+30]):]:ax.axis('off')
    fig.tight_layout();fig.savefig(args.output/f'selected-{start//30+1}.png',dpi=110);plt.close(fig)
print(json.dumps(summary))
