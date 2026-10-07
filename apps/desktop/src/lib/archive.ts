import { api, type RevisionDto } from './api';

/** Read the complete immutable revision, abandoning stale UI requests between chunks. */
export async function readRevision(entryId:string, revision:RevisionDto, current:()=>boolean = ()=>true):Promise<RevisionDto> {
  if (revision.content_inline !== false) return revision;
  const read = async (field:'body'|'document'|'metadata') => {
    let offset = 0;
    const chunks:string[] = [];
    do {
      if (!current()) throw new Error('stale archive request');
      const part = await api.archiveRevisionRead(entryId,revision.revision_id,field,offset);
      if (!current()) throw new Error('stale archive request');
      chunks.push(part.text);
      if (part.next_offset === null) return chunks.join('');
      if (part.next_offset <= offset) throw new Error('non-progressing archive response');
      offset = part.next_offset;
    } while (true);
  };
  const [body,document,metadata] = await Promise.all([read('body'),read('document'),read('metadata')]);
  return {...revision,body,document:JSON.parse(document),metadata:JSON.parse(metadata),content_inline:true};
}

export function mergeMedia<T extends {media_id:string;variant_id:string|null}>(before:T[], incoming:T[]):T[] {
  const merged = new Map(before.map(m=>[`${m.media_id}/${m.variant_id??''}`,m]));
  for (const m of incoming) merged.set(`${m.media_id}/${m.variant_id??''}`,m);
  return [...merged.values()];
}
