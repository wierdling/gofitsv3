'use strict';
// Shared by canvas gestures and the small Node coordinate regression tests.
const LabelPosition = {
 overviewPoint(width,height,rect,clientX,clientY){return {x:Math.max(0,Math.min(width,(clientX-rect.left)*width/rect.width)),y:Math.max(0,Math.min(height,height-(clientY-rect.top)*height/rect.height))};},
 rectangle(a,b,width,height){const x=Math.max(0,Math.floor(Math.min(a.x,b.x))),y=Math.max(0,Math.floor(Math.min(a.y,b.y)));return {x,y,width:Math.min(width,Math.ceil(Math.max(a.x,b.x)))-x,height:Math.min(height,Math.ceil(Math.max(a.y,b.y)))-y};},
 clamp(region, x, y) {
  return {x:Math.max(region.x,Math.min(region.x+region.width-1,x)),y:Math.max(region.y,Math.min(region.y+region.height-1,y))};
 },
 fromClient(region, rect, clientX, clientY) {
  return this.clamp(region,(clientX-rect.left)*region.width/rect.width+region.x-.5,region.y+region.height-.5-(clientY-rect.top)*region.height/rect.height);
 },
 dragged(region, original, start, end) {
  return this.clamp(region,original.x+end.x-start.x,original.y+end.y-start.y);
 }
};
if (typeof module !== 'undefined') module.exports=LabelPosition;
