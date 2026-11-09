// Transport interruptions do not discard observations. Only an explicit controller reset does.
export class ConnectionState {
 constructor(){this.reset();}
 reset(){this.snapshot=null;this.connected=false;this.interruptedAt=null;}
 receive(event){const reset=!!event.reset&&this.snapshot!==null;if(reset)this.reset();if(event.snapshot)this.snapshot=event.snapshot;this.connected=!!event.connected;return reset;}
 view(localConnected,authorized=true,now=Date.now()){
  const measured=new Date(this.snapshot?.observedAt).getTime();const fresh=!!this.snapshot&&Number.isFinite(measured)&&now-measured<15000;
  const stale=!!this.snapshot&&!fresh;
  const live=!!(authorized&&localConnected&&this.connected&&fresh);
  if(live)this.interruptedAt=null;else this.interruptedAt??=now;
  const elapsed=now-this.interruptedAt;const grace=authorized&&fresh&&elapsed<3000;
  const label=!authorized?'Session required':live||grace?'Up to date':this.snapshot?elapsed<30000?'Reconnecting':'Updates paused':elapsed<30000?'Connecting':'Unable to connect';
  const detail=live?'Receiving shared controller observations.':!authorized?'Open the complete URL printed by hostlier ui.':!localConnected?'Reconnecting to your local CLI. Last received data stays visible.':!this.connected?'Reconnecting to the controller. Last received data stays visible.':'Waiting for a fresh controller capture.';
  return {snapshot:this.snapshot,stale,live,label,detail,tone:live||grace?'live':'idle',caption:stale?'Stale · showing last capture':live||grace?'Updates automatically':this.snapshot?elapsed<30000?'Reconnecting…':'Showing last received data':'Waiting for first capture'};
 }
}
