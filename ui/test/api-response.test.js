import test from 'node:test';
import assert from 'node:assert/strict';
import {readAPIResponse,sessionRequiredMessage} from '../src/api-response.js';
test('expired plaintext sessions never reach JSON parsing',async()=>{
 for(const status of [401,403]){let parsed=false;const response={status,ok:false,json:async()=>{parsed=true;throw SyntaxError('Unexpected token L');}};
 await assert.rejects(readAPIResponse(response),error=>error.code==='UI_SESSION_REQUIRED'&&error.message===sessionRequiredMessage);assert.equal(parsed,false);}
});
test('plaintext proxy failures use a readable safe message',async()=>{
 await assert.rejects(readAPIResponse({status:502,ok:false,json:async()=>{throw SyntaxError('private proxy response');}}),error=>error.code==='UI_RESPONSE'&&error.message.includes('502')&&!error.message.includes('private'));
});
test('ordinary successful data and structured controller errors retain their contract',async()=>{
 assert.deepEqual(await readAPIResponse({status:200,ok:true,json:async()=>({applications:[]})}),{applications:[]});
 await assert.rejects(readAPIResponse({status:400,ok:false,json:async()=>({error:{code:'CONNECTION',message:'Controller unreachable'}})}),error=>error.code==='CONNECTION'&&error.message==='Controller unreachable');
});
