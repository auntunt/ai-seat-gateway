const esc=v=>String(v??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
const inline=s=>esc(s).replace(/\*\*([^*]+)\*\*/g,'<strong>$1</strong>');
export function reportSections(text){
 const sections=[];let current={title:'',lines:[]};
 for(const line of String(text||'').split('\n')){const heading=line.match(/^\s*#{1,4}\s+(.+)$/);if(heading){if(current.title||current.lines.some(x=>x.trim()))sections.push(current);current={title:heading[1],lines:[]}}else current.lines.push(line)}
 if(current.title||current.lines.some(x=>x.trim()))sections.push(current);return sections;
}
function body(lines){return lines.map(line=>{if(!line.trim())return '';const bullet=line.match(/^\s*[-*]\s+(.*)$/);return `<p${bullet?' class="pr-point"':''}>${inline(bullet?bullet[1]:line)}</p>`}).join('')}
export function reportHTML(text){const sections=reportSections(text);return `<div class="pr-report">${sections.map((s,i)=>`<section class="pr-section ${i===0?'pr-lead':''}">${s.title?`<h3>${inline(s.title)}</h3>`:''}<div>${body(s.lines)}</div></section>`).join('')}</div>`}
export function evidenceHTML(clues){
 const groups=[['problem','问题与待确认'],['practice','方法与经验'],['skill','可复用技能'],['project','项目进展线索']];
 const known=new Set(groups.map(x=>x[0]));if(clues.some(x=>!known.has(x.category)))groups.push(['other','其他线索']);
 return `<div class="ei-evidence-groups">${groups.map(([key,title])=>{const items=clues.filter(x=>key==='other'?!known.has(x.category):x.category===key);if(!items.length)return '';return `<section class="ei-evidence-group"><div class="ei-group-heading"><h3>${title}</h3><span>${items.length} 条</span></div>${items.map(c=>`<article class="ei-evidence-item"><div class="ei-source-line"><time>${esc(c.source_day)}</time>${c.project?`<span>${esc(c.project)}</span>`:''}</div><h4>${esc(c.title)}</h4><p>${esc(c.content)}</p><details><summary>核对来源 · ${c.verified?'引文与日报匹配':'引文待核对'}</summary>${c.quote?`<blockquote>${esc(c.quote)}</blockquote>`:'<p>此线索未保存引用片段，请结合下方完整日报核对。</p>'}</details></article>`).join('')}</section>`}).join('')||'<p class="ei-empty">所选日期还没有已提取的工作线索。可先查看工作日报，或生成这段时间的工作画像。</p>'}</div>`
}
