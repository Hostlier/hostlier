export const sessionRequiredMessage='Your dashboard session has expired. Open the current link printed by hostlier ui to reconnect this browser.';
export class DashboardRequestError extends Error {
 constructor(message,code,status){super(message);this.name='DashboardRequestError';this.code=code;this.status=status;}
}
// Authentication and proxy failures may be plain text. Never expose their parser errors or raw body.
export async function readAPIResponse(response){
 if(response.status===401||response.status===403)throw new DashboardRequestError(sessionRequiredMessage,'UI_SESSION_REQUIRED',response.status);
 let result;
 try{result=await response.json();}
 catch{throw new DashboardRequestError(response.ok?'The dashboard received an invalid response. Retry shortly.':`Dashboard request failed (HTTP ${response.status}). Retrying when the connection returns.`,'UI_RESPONSE',response.status);}
 if(!response.ok)throw new DashboardRequestError(typeof result?.error?.message==='string'?result.error.message:`Dashboard request failed (HTTP ${response.status}).`,result?.error?.code||'UI_REQUEST',response.status);
 return result;
}
