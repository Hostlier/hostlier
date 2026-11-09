import test from 'node:test';
import assert from 'node:assert/strict';
import {commandLine,shellArgument} from '../src/primitives.js';
test('CLI handoffs quote server and workload data and retain explicit host selection',()=>{
 assert.equal(commandLine(['status','faq-demo'],{name:'linux-vm'},'worker'),'hostlier status faq-demo --server linux-vm --on worker');
 assert.equal(shellArgument('app; echo unsafe'),"'app; echo unsafe'");
 assert.equal(shellArgument("a'b"),"'a'\"'\"'b'");
 assert.match(commandLine(['status','app'],{name:'$(unexpected)'}),/--server '\$\(unexpected\)'$/);
});
test('CLI handoffs preserve the running binary, profile directory and private CA without embedding credentials',()=>{
 const line=commandLine(['status','app'],{name:'vm',cli:{binary:'/tmp/hostlier ui',home:'/tmp/private profiles',caFile:'/tmp/ca.pem'}});
 assert.equal(line,"HOSTLIER_HOME='/tmp/private profiles' HOSTLIER_CA_FILE=/tmp/ca.pem '/tmp/hostlier ui' status app --server vm");
});
