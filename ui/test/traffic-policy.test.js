import test from 'node:test';
import assert from 'node:assert/strict';
import {trafficPolicyRows} from '../src/deployment-details.js';

test('traffic policy inventory preserves RPC identities and inherited budgets',()=>{
 const rows=trafficPolicyRows([{name:'sdk',idleTimeoutSeconds:900,grpc:{services:{'engine.v1.EngineService':{idleTimeoutSeconds:300,methods:{Connect:{idleTimeoutSeconds:60},SubscribeWebhooks:{}}}}}}]);
 assert.deepEqual(rows.map(row=>[row.path,row.timeout]),[['/engine.v1.EngineService/Connect','60s'],['/engine.v1.EngineService/SubscribeWebhooks','300s']]);
});

test('HTTP policy inventory distinguishes WebSocket upgrade and HTTP/3 admission',()=>{
 const rows=trafficPolicyRows([{name:'web',http:{routes:{socket:{path:'/ws',methods:['GET'],protocol:'websocket'},events:{path:'/events',methods:['GET'],protocol:'sse',httpVersions:['h3']}}}}]);
 assert.deepEqual(rows.map(row=>[row.path,row.versions,row.methods]),[['/ws','h1','GET'],['/events','h3','GET']]);
});
