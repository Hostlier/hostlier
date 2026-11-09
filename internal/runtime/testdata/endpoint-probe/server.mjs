// Protocol fixture only: requests exercise a real Hostlier deployment and edge, without provider credentials.
import http from 'node:http';
import http2 from 'node:http2';
import {createHash} from 'node:crypto';
import {readFileSync} from 'node:fs';
const revision=()=>readFileSync('/app/revision.txt','utf8').trim();
// Emit separated events so clients can detect response buffering, cancellation and retained headers.
function events(req,res){
 res.writeHead(200,{'content-type':'text/event-stream','cache-control':'no-cache','Mcp-Session-Id':'hostlier-probe-session'});
 res.write('event: message\ndata: {"sequence":1}\n\n');
 const timer=setTimeout(()=>res.end('event: message\ndata: {"sequence":2}\n\n'),1000);
 res.on('close',()=>clearTimeout(timer));
}
// Stateful method/header checks model Streamable HTTP transport without implementing an MCP application.
function mcp(req,res){
 if(req.method==='DELETE'){res.writeHead(req.headers['mcp-session-id']==='hostlier-probe-session'?204:400);return res.end();}
 if(req.method==='GET')return events(req,res);
 let body='';req.on('data',chunk=>body+=chunk);req.on('end',()=>{
  const input=JSON.parse(body);
  if(input.method==='initialize'){res.writeHead(200,{'content-type':'application/json','Mcp-Session-Id':'hostlier-probe-session'});return res.end(JSON.stringify({jsonrpc:'2.0',id:input.id,result:{protocolVersion:'2025-03-26',capabilities:{},serverInfo:{name:'hostlier-probe',version:'1'}}}));}
  if(req.headers['mcp-session-id']!=='hostlier-probe-session'){res.writeHead(400);return res.end();}
  return events(req,res);
 });
}
// A request body stays open while its first chunk must already be visible in the response.
const server=http.createServer((req,res)=>{
 if(req.url==='/sse')return events(req,res);
 if(req.url==='/mcp')return mcp(req,res);
 if(req.url==='/duplex'){res.writeHead(200,{'content-type':'application/octet-stream'});req.on('data',chunk=>res.write(chunk));req.on('end',()=>res.end());return;}
 res.writeHead(200,{'content-type':'text/plain'});res.end(revision()+'\n');
});
// Echo one bounded RFC 6455 text message; the standard client owns masking, framing and close validation.
server.on('upgrade',(req,socket)=>{
 const accept=createHash('sha1').update(req.headers['sec-websocket-key']+'258EAFA5-E914-47DA-95CA-C5AB0DC85B11').digest('base64');
 socket.write('HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: '+accept+'\r\n\r\n');
 let buffered=Buffer.alloc(0);
 socket.on('data',chunk=>{
  buffered=Buffer.concat([buffered,chunk]);if(buffered.length<6)return;
  const length=buffered[1]&127;if(length>125)return socket.destroy();if(buffered.length<length+6)return;
  const mask=buffered.subarray(2,6);const message=Buffer.from(buffered.subarray(6,6+length));for(let i=0;i<length;i++)message[i]^=mask[i%4];
  const opcode=buffered[0]&15;buffered=buffered.subarray(6+length);socket.write(Buffer.concat([Buffer.from([128|(opcode===9?10:opcode),length]),message]));if(opcode===8)socket.end();
 });
});
server.listen(8081,'0.0.0.0');
// Native HTTP/2 gRPC health Check and Watch include actual framed protobuf messages and trailers.
const grpc=http2.createServer();
grpc.on('stream',(stream,headers)=>{
 stream.respond({':status':200,'content-type':'application/grpc'},{waitForTrailers:true});
 stream.on('wantTrailers',()=>stream.sendTrailers({'grpc-status':'0'}));
 stream.on('error',()=>{});stream.resume();
 stream.write(Buffer.from([0,0,0,0,2,8,1]));
 if(String(headers[':path']).endsWith('/Watch'))setTimeout(()=>{if(!stream.destroyed)stream.end(Buffer.from([0,0,0,0,2,8,1]));},1000);else stream.end();
});
grpc.listen(50051,'0.0.0.0');
