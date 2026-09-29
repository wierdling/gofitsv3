const test=require('node:test');
const assert=require('node:assert/strict');
const position=require('./web/label-position.js');
test('pointer coordinates retain FITS pixel centers across zoom and scroll',()=>{
 const region={x:200,y:300,width:512,height:256};
 for(const zoom of [.5,1,2,4]){
  const rect={left:-137.25,top:85.5,width:512*zoom,height:256*zoom};
  const world={x:248.25,y:431.75};
  const actual=position.fromClient(region,rect,rect.left+(world.x-region.x+.5)*zoom,rect.top+(region.y+region.height-.5-world.y)*zoom);
  assert.deepEqual(actual,world);
 }
});
test('drag preserves grab offset and clamps final point inside the region',()=>{
 const region={x:10,y:20,width:100,height:80};
 assert.deepEqual(position.dragged(region,{x:50.5,y:60.5},{x:52,y:59},{x:57,y:49}),{x:55.5,y:50.5});
 assert.deepEqual(position.dragged(region,{x:50,y:60},{x:50,y:60},{x:-500,y:500}),{x:10,y:99});
});
test('overview rectangles use upward FITS y and accept reverse drags',()=>{
 const rect={left:20,top:40,width:200,height:100};
 const a=position.overviewPoint(1000,500,rect,60,60),b=position.overviewPoint(1000,500,rect,160,110);
 assert.deepEqual(a,{x:200,y:400});assert.deepEqual(b,{x:700,y:150});
 assert.deepEqual(position.rectangle(a,b,1000,500),{x:200,y:150,width:500,height:250});
 assert.deepEqual(position.rectangle(b,a,1000,500),position.rectangle(a,b,1000,500));
});
test('overview selections clamp outside pointers and cover fractional pixel bounds',()=>{
 const rect={left:0,top:0,width:100,height:100};
 const a=position.overviewPoint(1000,500,rect,-20,-20),b=position.overviewPoint(1000,500,rect,120,120);
 assert.deepEqual(position.rectangle(a,b,1000,500),{x:0,y:0,width:1000,height:500});
 assert.deepEqual(position.rectangle({x:20.3,y:40.7},{x:45.8,y:80.1},1000,500),{x:20,y:40,width:26,height:41});
});
