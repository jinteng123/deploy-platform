import { Inbox } from 'lucide-react'

export function EmptyState({ text = '暂无数据' }: { text?: string }) {
  return (
    <div className='flex flex-col items-center gap-2 py-8 text-muted-foreground'>
      <Inbox aria-hidden='true' className='size-6' />
      <p className='text-sm'>{text}</p>
    </div>
  )
}
