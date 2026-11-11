import assert from 'node:assert/strict'
import {readFileSync} from 'node:fs'
import {webcrypto} from 'node:crypto'
import vm from 'node:vm'
import ts from 'typescript'
const compile=p=>ts.transpileModule(readFileSync(new URL(p,import.meta.url),'utf8'),{compilerOptions:{module:ts.ModuleKind.CommonJS,target:ts.ScriptTarget.ES2022}}).outputText
for (const [status,code] of [[501,'protected_operation_unsupported'],[503,'protected_not_ready']]) {
 const context={exports:{},require:()=>({API_BASE_URL:'/api'}),crypto:webcrypto,structuredClone}
 vm.runInNewContext(compile('../src/pages/ReceivingPage/receivingScanCommands.ts'),context)
 const data=new Map(),calls=[]
 const storage={get length(){return data.size},key:i=>[...data.keys()][i]??null,getItem:k=>data.get(k)??null,setItem:(k,v)=>data.set(k,v),removeItem:k=>data.delete(k)}
 const id='aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa'
 const send=async(url,options)=>{if(url.endsWith('/deployment'))return new Response(JSON.stringify({deploymentId:id}));calls.push(options);return new Response(JSON.stringify({code}),{status})}
 const queue=new context.exports.ReceivingScanCommands(()=>storage,send)
 await queue.prepare(id)
 await assert.rejects(queue.scan(1,'SKU',async()=>{throw Error('must not synchronize')}),/сохранена/)
 const saved=[...data.entries()];assert.equal(saved.length,1);assert.equal(calls.length,1)
 await new Promise(resolve=>setImmediate(resolve));assert.equal(calls.length,1,'no automatic retries')
 await assert.rejects(queue.scan(1,'OTHER',async()=>{}));assert.deepEqual([...data.entries()],saved)
 await assert.rejects(queue.recover(async()=>{}),/сохранена/)
 assert.equal(calls[1].body,calls[0].body,'manual recovery preserves UUID');assert.deepEqual([...data.entries()],saved)
 // Exercise the actual assembly hook: unsuccessful writes retain local state and intent.
 let si=0,ri=0,mounted=false;const states=[],refs=[],requests=[]
 const hooks={useState(initial){const i=si++;if(!(i in states))states[i]=initial;return [states[i],v=>{states[i]=v}]},useRef(initial){const i=ri++;return refs[i]??={current:initial}},useEffect(effect){if(!mounted){mounted=true;effect()}}}
 const hookContext={exports:{},require:k=>k==='react'?hooks:{API_BASE_URL:'/api'},crypto:webcrypto,AbortController,fetch:async(url,options)=>{if(!options?.method)return {ok:true,json:async()=>({version:0,state:{picks:[]}})};requests.push(options);return new Response(JSON.stringify({code}),{status})}}
 vm.runInNewContext(compile('../src/pages/AssemblyPage/useAssemblyDraft.ts'),hookContext)
 const tab=()=>{si=ri=0;return hookContext.exports.default({picks:[]})};tab();await new Promise(resolve=>setImmediate(resolve))
 assert.equal(await tab().assemblyAction('scan',{code:'SKU'}),false);assert.equal(tab().blocked,true);assert.match(tab().error,/сохранены/);assert.equal(tab().state.picks.length,0)
 await new Promise(resolve=>setImmediate(resolve));assert.equal(requests.length,1)
 assert.equal(await tab().retry(),false);assert.equal(requests[0].body,requests[1].body)
}
console.log('Protected API errors passed: 501/503, real receiving journal and assembly hook, retained UUID/local data, no false success or automatic retries.')
