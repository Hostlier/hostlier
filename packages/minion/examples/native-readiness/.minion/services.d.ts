// Generated fixture from the included synthetic readiness contract; minion prepare replaces it.
import '@fused/minion';
type MinionInput0 = {  };
type MinionOutput0 = { "status": string; "synthetic": boolean; };
declare module '@fused/minion' { interface ServiceContracts { "probe": { "ReadStatus": { input: MinionInput0; output: MinionOutput0 }; }; } }
