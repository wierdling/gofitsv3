'use strict';
const $ = id => document.getElementById(id);
const canvas = $('science'), ctx = canvas.getContext('2d');
const overview = $('overview'), ov = overview.getContext('2d');
let state, project, regionID, selected, sourceImage, overviewImage, overlay = null;
let edits = 0, saved = 0, saving = null, timer, busy = false, imageSerial = 0, drag = null, lastReport = null;
let runsKey = '';
let centerPreview = null;
let overviewDrag=null,overviewSelection=null,foreignWorkspace=false,browseParent='';
function clearCenterPreview(){centerPreview=null;$('applyCenter').disabled=true;$('cancelCenter').disabled=true;$('centerStatus').textContent='Select a star label to preview a center from science pixels. Detector catalogs are not used.';}
const colors = {star:'#65e4ff',nonstar:'#ffd374',ambiguous:'#ed9dff',unusable:'#ff788d'};
function error(e) { $('error').textContent = e.message || String(e); $('error').hidden = false; }
function clearError() { $('error').hidden = true; }
function current() { return project.regions.find(r => r.id === regionID); }
function uid() { return crypto.randomUUID(); }
function auditFrom(reply) {
 project.revision = reply.revision;
 for (const r of project.regions) { const p = reply.regions.find(v => v.id === r.id); if (p) { r.revealedAt=p.revealedAt; r.revealedBeforeReview=p.revealedBeforeReview; r.editedAfterReveal=p.editedAfterReveal; } }
 $('revision').textContent = `Labels revision ${project.revision}`;
 showAudit();
}
async function api(path, method='GET', body) {
 const headers=state?{'X-Benchmark-Token':state.token}:{};if(method!=='GET')headers['Content-Type']='application/json';
 const res = await fetch(path,{method,headers,body:body===undefined?undefined:JSON.stringify(body)});
 const value = await res.json(); if (!res.ok) throw new Error(value.error || `HTTP ${res.status}`); return value;
}
function changed() {
 edits++; $('saveStatus').textContent='Unsaved changes'; clearTimeout(timer);
 timer=setTimeout(()=>ensureSaved().catch(error),650); draw(); updateCount();
}
async function ensureSaved() {
 if (saving) return saving;
 saving=(async()=>{
  while (saved!==edits) {
   const serial=edits, snapshot=structuredClone(project);
   const reply=await api('/api/labels','PUT',snapshot);auditFrom(reply);saved=serial;
  }
  $('saveStatus').textContent='All labels saved';
 })();
 try { await saving; } finally { saving=null; }
}
async function action(work) {
 if (busy) return; busy=true; $('saveStatus').textContent='Working…'; document.querySelector('main').inert=true;
 try { clearError(); await ensureSaved(); await work(); } catch(e) { error(e); }
 finally { busy=false; document.querySelector('main').inert=false; $('saveStatus').textContent=saved===edits?'All labels saved':'Unsaved changes'; }
}
function populateRegions() {
 const select=$('region'); select.replaceChildren();
 for (const r of project.regions) select.add(new Option(`${r.reviewed?'✓ ':''}${r.name} · ${r.split==='validation'?'V':'D'}`,r.id));
 select.value=regionID;
}
function showAudit() {
 const r=current(); if (!r) return;
 $('audit').textContent=r.revealedAt?`Detections exposed ${new Date(r.revealedAt).toLocaleString()}.${r.revealedBeforeReview?' Revealed before full review.':''}${r.editedAfterReveal?' Labels edited after reveal.':''}`:'Detections have not been revealed for this region.';
 $('split').disabled=Boolean(r.revealedAt);
 $('deleteRegion').disabled=Boolean(r.revealedAt);
}
function updateCount() {
 const labels=project.labels.filter(l=>l.regionId===regionID), boxes=project.protected.filter(l=>l.regionId===regionID);
 $('labelCount').textContent=`${labels.filter(l=>l.kind==='star').length} stars · ${labels.length} object labels · ${boxes.length} protected areas`;
}
function selectMark(id) {
 clearCenterPreview();
 selected=id; const label=project.labels.find(l=>l.id===id);
 $('previewCenter').disabled=!label||label.kind!=='star';
 $('labelEditor').disabled=!label; $('deleteLabel').disabled=!id;
 if (label) { $('labelKind').value=label.kind; $('note').value=label.note||''; for(const cb of document.querySelectorAll('#tags input')) cb.checked=(label.tags||[]).includes(cb.value); }
 draw();
}
async function loadRegion() {
 drag=null;clearCenterPreview();
 const r=current(); if(!r) {sourceImage=null;draw();return;}
 overlay=null; $('overlayStatus').textContent='Detector hidden'; $('reveal').textContent='Reveal run A detections'; selected=null;
 $('regionName').value=r.name; $('split').value=r.split; $('reviewed').checked=r.reviewed;
 populateRegions(); showAudit(); updateCount(); selectMark(null); drawOverview();
 await loadImage();
}
async function loadImage() {
 const r=current(); if (!r) return; const serial=++imageSerial; sourceImage=null;
 canvas.width=r.width;canvas.height=r.height;applyZoom();draw();
 const img=new Image(); img.src=`/api/image?region=${encodeURIComponent(r.id)}&gain=${$('gain').value}&revision=${project.revision}&dataset=${state.dataset.sha256}`;
 try { await img.decode(); if (serial!==imageSerial) return; sourceImage=img; draw(); } catch { if(serial===imageSerial) error(new Error('Could not load science cutout.')); }
}
function applyZoom() { const scale=Number($('zoom').value);canvas.style.width=`${canvas.width*scale}px`;canvas.style.height=`${canvas.height*scale}px`; }
function focusPosition(p){if(!p)return;const [x,y]=screen(p.x,p.y),z=Number($('zoom').value);$('viewport').scrollLeft=x*z-$('viewport').clientWidth/2;$('viewport').scrollTop=y*z-$('viewport').clientHeight/2;}
function screen(x,y) { const r=current(); return [x-r.x+.5,r.y+r.height-.5-y]; }
function point(e) {return LabelPosition.fromClient(current(),canvas.getBoundingClientRect(),e.clientX,e.clientY);}
function displayedLabels(){return project.labels.filter(l=>l.regionId===regionID).map(l=>drag?.kind==='label'&&drag.id===l.id&&drag.moved?{...l,...LabelPosition.dragged(current(),drag.original,drag.start,drag.end)}:l);}
function draw() {
 ctx.clearRect(0,0,canvas.width,canvas.height);if(sourceImage)ctx.drawImage(sourceImage,0,0,canvas.width,canvas.height);
 const r=current();if(!r)return;
 if(overlay) {ctx.lineWidth=1;ctx.strokeStyle='#83ff8b';for(const s of overlay){if(s.Status!=='accepted')continue;const [x,y]=screen(s.X,s.Y);ctx.beginPath();ctx.arc(x,y,s.Radius,0,Math.PI*2);ctx.stroke();}}
 if($('showLabels').checked) {
  for(const b of project.protected.filter(b=>b.regionId===r.id)){ctx.strokeStyle=b.id===selected?'#fff':'#df87ec';ctx.fillStyle='#df87ec22';const x=b.x-r.x,y=r.y+r.height-b.y-b.height;ctx.fillRect(x,y,b.width,b.height);ctx.strokeRect(x+.5,y+.5,b.width-1,b.height-1);}
  for(const l of displayedLabels()){const [x,y]=screen(l.x,l.y);ctx.strokeStyle=l.id===selected?'white':colors[l.kind];ctx.lineWidth=1;ctx.beginPath();if(l.kind==='star'){ctx.arc(x,y,5,0,Math.PI*2);ctx.moveTo(x-8,y);ctx.lineTo(x-3,y);ctx.moveTo(x+3,y);ctx.lineTo(x+8,y);ctx.moveTo(x,y-8);ctx.lineTo(x,y-3);ctx.moveTo(x,y+3);ctx.lineTo(x,y+8);}else if(l.kind==='nonstar'){ctx.moveTo(x-5,y-5);ctx.lineTo(x+5,y+5);ctx.moveTo(x+5,y-5);ctx.lineTo(x-5,y+5);}else if(l.kind==='ambiguous'){ctx.moveTo(x,y-6);ctx.lineTo(x+6,y);ctx.lineTo(x,y+6);ctx.lineTo(x-6,y);ctx.closePath();}else ctx.rect(x-5,y-5,10,10);ctx.stroke();}
 }
 if(drag?.kind==='protected'){const a=screen(drag.start.x,drag.start.y),b=screen(drag.end.x,drag.end.y);ctx.strokeStyle='#fff';ctx.strokeRect(a[0],a[1],b[0]-a[0],b[1]-a[1]);}
 if(centerPreview){const a=screen(centerPreview.original.x,centerPreview.original.y),b=screen(centerPreview.x,centerPreview.y);ctx.strokeStyle='#ffe65a';ctx.lineWidth=1.5;ctx.setLineDash([3,3]);ctx.beginPath();ctx.moveTo(...a);ctx.lineTo(...b);ctx.stroke();ctx.setLineDash([]);ctx.beginPath();ctx.arc(b[0],b[1],7,0,Math.PI*2);ctx.moveTo(b[0]-4,b[1]);ctx.lineTo(b[0]+4,b[1]);ctx.moveTo(b[0],b[1]-4);ctx.lineTo(b[0],b[1]+4);ctx.stroke();}
}
function drawOverview() {
 if(!overviewImage)return;ov.clearRect(0,0,overview.width,overview.height);ov.drawImage(overviewImage,0,0,overview.width,overview.height);
 const sx=overview.width/state.dataset.width,sy=overview.height/state.dataset.height;
 for(const r of project.regions){const x=r.x*sx,y=(state.dataset.height-r.y-r.height)*sy;ov.strokeStyle=r.id===regionID?'#fff':r.split==='validation'?'#e1a6f7':'#6baef9';ov.lineWidth=r.id===regionID?2:1;ov.strokeRect(x,y,r.width*sx,r.height*sy);ov.font='11px system-ui';ov.fillStyle=ov.strokeStyle;ov.fillText(r.id,x+2,y+12);}
 const draft=overviewDrag?LabelPosition.rectangle(overviewDrag.start,overviewDrag.end,state.dataset.width,state.dataset.height):overviewSelection;
 if(draft){ov.strokeStyle='#62ffd7';ov.fillStyle='#62ffd733';ov.lineWidth=2;const x=draft.x*sx,y=(state.dataset.height-draft.y-draft.height)*sy;ov.fillRect(x,y,draft.width*sx,draft.height*sy);ov.strokeRect(x,y,draft.width*sx,draft.height*sy);}
}
canvas.addEventListener('pointerdown',e=>{
 if(busy||!sourceImage||e.button!==0||drag)return; const p=point(e); if($('tool').value==='protected'){clearCenterPreview();drag={kind:'protected',start:p,end:p,pointerId:e.pointerId};canvas.setPointerCapture(e.pointerId);draw();return;}
 const nearby=project.labels.filter(l=>l.regionId===regionID).map(l=>({l,d:Math.hypot(l.x-p.x,l.y-p.y)})).sort((a,b)=>a.d-b.d)[0];
 if(!e.shiftKey&&nearby&&nearby.d<Math.min(6,10/Number($('zoom').value))){selectMark(nearby.l.id);drag={kind:'label',id:nearby.l.id,original:{x:nearby.l.x,y:nearby.l.y},start:p,end:p,clientX:e.clientX,clientY:e.clientY,moved:false,pointerId:e.pointerId};canvas.setPointerCapture(e.pointerId);return;}
 if($('tool').value==='select'){const b=project.protected.find(b=>b.regionId===regionID&&p.x>=b.x&&p.x<b.x+b.width&&p.y>=b.y&&p.y<b.y+b.height);selectMark(b?.id||null);return;}
 const l={id:uid(),regionId:regionID,x:p.x,y:p.y,kind:$('tool').value,tags:[],note:''};project.labels.push(l);changed();selectMark(l.id);
});
canvas.addEventListener('pointermove',e=>{if(!current())return;const p=point(e);$('coordinates').textContent=`x ${p.x.toFixed(2)} · y ${p.y.toFixed(2)} · zero-based FITS pixels`;if(drag&&drag.pointerId===e.pointerId){drag.end=p;if(drag.kind==='label'&&Math.hypot(e.clientX-drag.clientX,e.clientY-drag.clientY)>=3)drag.moved=true;draw();}});
canvas.addEventListener('pointerup',e=>{if(!drag||drag.pointerId!==e.pointerId)return;const gesture=drag,end=point(e),start=drag.start;drag=null;
 if(gesture.kind==='label'){const l=project.labels.find(l=>l.id===gesture.id);if(l&&gesture.moved){Object.assign(l,LabelPosition.dragged(current(),gesture.original,start,end));changed();}draw();return;}
 const x=Math.floor(Math.min(start.x,end.x)),y=Math.floor(Math.min(start.y,end.y));const b={id:uid(),regionId:regionID,x,y,width:Math.floor(Math.max(start.x,end.x))-x+1,height:Math.floor(Math.max(start.y,end.y))-y+1};if(b.width>1&&b.height>1){project.protected.push(b);changed();selectMark(b.id);}draw();});
canvas.addEventListener('pointercancel',()=>{drag=null;draw();});
canvas.addEventListener('lostpointercapture',()=>{drag=null;draw();});
overview.addEventListener('click',e=>{if(busy||$('drawRegion').checked)return;const rect=overview.getBoundingClientRect(),x=(e.clientX-rect.left)/rect.width*state.dataset.width,y=state.dataset.height-(e.clientY-rect.top)/rect.height*state.dataset.height;const r=project.regions.find(r=>x>=r.x&&x<r.x+r.width&&y>=r.y&&y<r.y+r.height);if(r){regionID=r.id;loadRegion().catch(error);}});
function overviewPoint(e){return LabelPosition.overviewPoint(state.dataset.width,state.dataset.height,overview.getBoundingClientRect(),e.clientX,e.clientY);}
overview.addEventListener('pointerdown',e=>{if(busy||!overviewImage||!$('drawRegion').checked||e.button!==0)return;const p=overviewPoint(e);overviewDrag={start:p,end:p,id:e.pointerId};overviewSelection=null;overview.setPointerCapture(e.pointerId);drawOverview();});
overview.addEventListener('pointermove',e=>{if(overviewDrag?.id===e.pointerId){overviewDrag.end=overviewPoint(e);drawOverview();}});
overview.addEventListener('pointerup',e=>{if(overviewDrag?.id!==e.pointerId)return;overviewSelection=LabelPosition.rectangle(overviewDrag.start,overviewPoint(e),state.dataset.width,state.dataset.height);overviewDrag=null;const r=overviewSelection;for(const [id,value]of Object.entries({newX:r.x,newY:r.y,newW:r.width,newH:r.height}))$(id).value=value;$('newRegionPanel').open=true;$('regionDraftStatus').textContent=`Proposed (${r.x}, ${r.y}), ${r.width} × ${r.height} pixels. Choose a name and split, then Add region.`;drawOverview();});
overview.addEventListener('pointercancel',()=>{overviewDrag=null;drawOverview();});
overview.addEventListener('lostpointercapture',()=>{overviewDrag=null;drawOverview();});
$('drawRegion').onchange=()=>{overviewDrag=null;overviewSelection=null;drawOverview();};
$('region').onchange=()=>{regionID=$('region').value;loadRegion().catch(error);};
$('regionName').onchange=()=>{current().name=$('regionName').value;changed();populateRegions();};
$('split').onchange=()=>{current().split=$('split').value;changed();populateRegions();drawOverview();};
$('reviewed').onchange=()=>{current().reviewed=$('reviewed').checked;changed();populateRegions();};
$('addRegion').onclick=()=>action(async()=>{
 const r={id:`r-${uid().slice(0,8)}`,name:$('newName').value,x:Number($('newX').value),y:Number($('newY').value),width:Number($('newW').value),height:Number($('newH').value),split:$('newSplit').value,reviewed:false};
 const next=structuredClone(project);next.regions.push(r);project=await api('/api/labels','PUT',next);edits=saved=0;regionID=r.id;overviewSelection=null;$('drawRegion').checked=false;$('regionDraftStatus').textContent='Region added. Enable drawing to add another.';auditFrom(project);await loadRegion();
});
$('deleteRegion').onclick=()=>action(async()=>{const next=structuredClone(project);next.regions=next.regions.filter(r=>r.id!==regionID);if(!next.regions.length)throw new Error('Keep at least one region.');next.labels=next.labels.filter(l=>l.regionId!==regionID);next.protected=next.protected.filter(l=>l.regionId!==regionID);project=await api('/api/labels','PUT',next);edits=saved=0;regionID=project.regions[0].id;auditFrom(project);await loadRegion();});
function editLabel(){const l=project.labels.find(l=>l.id===selected);if(!l)return;l.kind=$('labelKind').value;l.note=$('note').value;l.tags=Array.from(document.querySelectorAll('#tags input:checked'),cb=>cb.value);clearCenterPreview();$('previewCenter').disabled=l.kind!=='star';changed();}
$('labelKind').onchange=editLabel;$('note').oninput=editLabel;for(const cb of document.querySelectorAll('#tags input'))cb.onchange=editLabel;
function deleteMark(){if(!selected||busy)return;project.labels=project.labels.filter(l=>l.id!==selected);project.protected=project.protected.filter(l=>l.id!==selected);changed();selectMark(null);}
$('deleteLabel').onclick=deleteMark;
$('save').onclick=()=>action(async()=>{});
$('gain').onchange=()=>loadImage().catch(error);$('zoom').onchange=()=>{applyZoom();focusPosition(centerPreview||project.labels.find(l=>l.id===selected));draw();};$('showLabels').onchange=draw;
document.addEventListener('keydown',e=>{if(busy)return;if(e.key==='Escape'){drag=null;overviewDrag=null;overviewSelection=null;clearCenterPreview();draw();drawOverview();return;}if(['INPUT','TEXTAREA','SELECT'].includes(document.activeElement.tagName))return;if(e.key==='Delete'){deleteMark();e.preventDefault();}const tools=['star','nonstar','ambiguous','unusable','protected','select'];if(Number(e.key)>=1&&Number(e.key)<=6)$('tool').value=tools[Number(e.key)-1];});
$('previewCenter').onclick=()=>action(async()=>{clearCenterPreview();draw();const l=project.labels.find(l=>l.id===selected);if(!l||l.kind!=='star')return;const preview=await api('/api/center-preview','POST',{regionId:regionID,x:l.x,y:l.y,radius:Number($('centerRadius').value)});centerPreview={...preview,id:l.id,original:{x:l.x,y:l.y}};$('applyCenter').disabled=false;$('cancelCenter').disabled=false;$('centerStatus').textContent=`Yellow: proposed (${preview.x.toFixed(2)}, ${preview.y.toFixed(2)}), shift ${preview.shift.toFixed(2)} pixels. ${preview.message}`;const [x,y]=screen(preview.x,preview.y),z=Number($('zoom').value);$('viewport').scrollLeft=x*z-$('viewport').clientWidth/2;$('viewport').scrollTop=y*z-$('viewport').clientHeight/2;draw();});
$('applyCenter').onclick=()=>{if(busy||!centerPreview)return;const l=project.labels.find(l=>l.id===centerPreview.id);if(l&&l.x===centerPreview.original.x&&l.y===centerPreview.original.y){l.x=centerPreview.x;l.y=centerPreview.y;changed();}clearCenterPreview();draw();};
$('cancelCenter').onclick=()=>{clearCenterPreview();draw();};
$('centerRadius').onchange=()=>{clearCenterPreview();draw();};
function updateRuns(next) {
 const key=JSON.stringify(next.runs);
 if(key!==runsKey){
  runsKey=key;const a=$('runA').value,b=$('runB').value;state.runs=next.runs;
  $('runA').replaceChildren();$('runB').replaceChildren(new Option('None',''));
  for(const r of state.runs){$('runA').add(new Option(r.name,r.id));$('runB').add(new Option(r.name,r.id));}
  if(state.runs.some(r=>r.id===a))$('runA').value=a;if(state.runs.some(r=>r.id===b))$('runB').value=b;
 }
 $('run').disabled=next.job.running;$('cancel').disabled=!next.job.running;$('job').textContent=next.job.message||'Ready';
 const available=next.evidence!=='mosaic-only';
 $('verifyOriginals').disabled=!available;if(!available)$('verifyOriginals').checked=false;
 $('evidence').textContent=available?'Original-exposure verification is optional and requires two independent confirmations. Unchecked runs use only the mosaic, without native saturation verification.':'Original-exposure verification is unavailable for this image. New runs use only the mosaic.';
 runDetails();
}
function runDetails(){const r=state.runs.find(r=>r.id===$('runA').value);$('runDetails').textContent=r?`${r.evidenceMode} · SNR ${r.minSNR} · residual ${r.maxResidual} · FWHM ${r.fwhm.toFixed(2)} · ${r.origin}`:'No runs yet.';}
$('runA').onchange=()=>{overlay=null;draw();$('reveal').textContent='Reveal run A detections';$('overlayStatus').textContent='Detector hidden';runDetails();};
$('reveal').onclick=()=>action(async()=>{if(overlay){overlay=null;$('reveal').textContent='Reveal run A detections';$('overlayStatus').textContent='Detector hidden';draw();return;}const reply=await api('/api/reveal','POST',{regionId:regionID,runId:$('runA').value,revision:project.revision});auditFrom(reply.project);overlay=reply.sources;$('reveal').textContent='Hide detections';$('overlayStatus').textContent='Green circles: automatically accepted footprints';draw();});
const pct=v=>v===null||v===undefined?'—':`${(100*v).toFixed(2)}%`;
function element(tag,text,parent){const el=document.createElement(tag);if(text!==undefined)el.textContent=text;if(parent)parent.append(el);return el;}
function table(parent,head,rows){const t=element('table',undefined,parent),h=element('tr',undefined,element('thead',undefined,t));head.forEach(v=>element('th',v,h));const b=element('tbody',undefined,t);for(const row of rows){const tr=element('tr',undefined,b);row.forEach(v=>element('td',v,tr));}}
function showReports(result){
 lastReport=result;$('downloadReport').disabled=false;const root=$('results');root.replaceChildren();
 element('p',`Frozen report ${result.id} · labels revision ${result.labels.revision}`,root).className='hint';
 const rows=[];for(const report of result.reports){const run=state.runs.find(r=>r.id===report.runId);for(const split of ['development','validation']){const c=report.splits[split];if(!report.regions.some(r=>r.region.split===split))continue;rows.push([run?.name||report.runId,split,c.truePositive,c.falsePositive,c.falseNegative,c.unresolved,pct(c.conservativePrecision),pct(c.recall)]);}}
 table(root,['Run','Split','TP','FP','Missed','Unresolved','Precision lower bound','Recall'],rows);
 for(const report of result.reports){const title=state.runs.find(r=>r.id===report.runId)?.name||report.runId;const details=element('details',undefined,root);element('summary',title+' — strata, protected areas, and errors',details);
  for(const [split,items]of Object.entries(report.strata)){if(!items.some(v=>v.truth))continue;element('p',split,details);table(details,['Star category','Recovered','Labeled','Recall'],items.map(v=>[v.tag,v.recovered,v.truth,pct(v.recall)]));}
  table(details,['Region','Protected valid pixels','Mask > 5%','Mean mask weight','Boundary labels excluded'],report.regions.map(r=>[r.region.name,r.protectedPixels,r.touchedPixels,r.protectedPixels?pct(r.maskWeight/r.protectedPixels):'—',r.ignoredBoundaryLabels]));
  const errors=element('div',undefined,details);errors.className='errors';for(const e of report.errors){const b=element('button',`${e.kind} (${e.x.toFixed(1)}, ${e.y.toFixed(1)})`,errors);b.onclick=async()=>{regionID=e.regionId;await loadRegion();const [x,y]=screen(e.x,e.y),z=Number($('zoom').value);$('viewport').scrollLeft=x*z-$('viewport').clientWidth/2;$('viewport').scrollTop=y*z-$('viewport').clientHeight/2;};}
  for(const warning of report.warnings)element('p',warning,details).className='hint warning';
 }
}
$('evaluate').onclick=()=>action(async()=>{const ids=[$('runA').value];if($('runB').value&&$('runB').value!==ids[0])ids.push($('runB').value);const reply=await api('/api/report','POST',{runIds:ids,tolerance:Number($('tolerance').value),split:$('scoreSplit').value,revision:project.revision});auditFrom(reply.project);showReports(reply.result);});
$('downloadReport').onclick=()=>{if(!lastReport)return;const url=URL.createObjectURL(new Blob([JSON.stringify(lastReport,null,2)],{type:'application/json'}));const a=document.createElement('a');a.href=url;a.download=`benchmark-${lastReport.id}.json`;a.click();setTimeout(()=>URL.revokeObjectURL(url),1000);};
$('run').onclick=()=>action(async()=>{await api('/api/runs','POST',{name:$('runName').value,minSNR:Number($('snr').value),maxResidual:Number($('residual').value),fwhm:Number($('fwhm').value),verifyOriginals:$('verifyOriginals').checked});updateRuns(await api('/api/state'));});
$('cancel').onclick=()=>api('/api/cancel','POST',{}).catch(error);
window.addEventListener('beforeunload',e=>{if(edits!==saved){e.preventDefault();e.returnValue='';}});
function downloadLabels(){const url=URL.createObjectURL(new Blob([JSON.stringify(project,null,2)],{type:'application/json'}));const a=document.createElement('a');a.href=url;a.download='labels.json';a.click();setTimeout(()=>URL.revokeObjectURL(url),1000);}
$('exportLabels').onclick=e=>{e.preventDefault();downloadLabels();};
$('openFile').onclick=()=>action(async()=>{$('saveStatus').textContent='Loading FITS and saved benchmark…';await api('/api/open','POST',{path:$('filePath').value,revision:project.revision});location.reload();});
async function browse(path){const reply=await api('/api/files?path='+encodeURIComponent(path||''));$('fileBrowser').hidden=false;$('browseDirectory').value=reply.path;browseParent=reply.parent;$('fileEntries').replaceChildren();for(const entry of reply.files){const b=element('button',(entry.directory?'Folder: ':'FITS: ')+entry.name,$('fileEntries'));b.onclick=()=>{if(entry.directory)browse(entry.path).catch(error);else{$('filePath').value=entry.path;$('fileBrowser').hidden=true;}};}if(!reply.files.length)element('p','No FITS files or subdirectories here.',$('fileEntries'));}
$('browseFiles').onclick=()=>browse('').catch(error);$('browseGo').onclick=()=>browse($('browseDirectory').value).catch(error);$('browseParent').onclick=()=>browse(browseParent).catch(error);
async function init(){state=await api('/api/state');project=state.project;regionID=project.regions[0]?.id;$('dataset').textContent=`${state.dataset.filter} · ${state.dataset.width} × ${state.dataset.height} · ${state.dataset.sciencePath}`;$('filePath').value=state.dataset.sciencePath;updateRuns(state);auditFrom(project);await loadRegion();overviewImage=new Image();overviewImage.src='/api/image?region=overview&gain=20&dataset='+state.dataset.sha256;await overviewImage.decode();overview.width=320;overview.height=Math.round(320*state.dataset.height/state.dataset.width);drawOverview();$('saveStatus').textContent='All labels saved';setInterval(async()=>{try{if(foreignWorkspace)return;const next=await api('/api/state');if(next.token!==state.token&&!busy){if(edits===saved){location.reload();return;}foreignWorkspace=true;clearTimeout(timer);busy=true;document.querySelector('main').inert=true;$('openFilePanel').inert=true;$('save').disabled=true;throw new Error('The active file changed in another window. Export your local labels before reloading.');}if(!busy)updateRuns(next);}catch(e){error(e);}},2000);}
init().catch(error);
