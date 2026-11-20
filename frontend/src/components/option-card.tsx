import { type LucideIcon } from 'lucide-react'
import { cn } from '@/lib/utils'

// Selectable option card used for radio-group choices (环境层级 /
// 连接方式 / 读写模式). Renders a <button role='radio'> — the caller
// wraps the group in role='radiogroup' with an aria-label.
export function OptionCard({
  icon: Icon,
  title,
  desc,
  selected,
  tone = 'blue',
  muted,
  disabled,
  onClick,
}: {
  icon: LucideIcon
  title: string
  desc: string
  selected: boolean
  tone?: 'blue' | 'red'
  muted?: boolean
  disabled?: boolean
  onClick: () => void
}) {
  return (
    <button
      type='button'
      role='radio'
      aria-checked={selected}
      aria-label={title}
      disabled={disabled}
      onClick={onClick}
      className={cn(
        'rounded-xl border p-4 text-start transition',
        'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[#234883]/40',
        'disabled:cursor-not-allowed disabled:opacity-60',
        selected
          ? tone === 'red'
            ? 'border-[#c2404e] bg-[#fdf6f7] ring-1 ring-[#c2404e]'
            : 'border-[#234883] bg-[#f3f7fd] ring-1 ring-[#234883]'
          : 'border-[#e1e9f5] bg-white hover:border-[#9bb6dd]'
      )}
    >
      <Icon
        className={cn(
          'size-5',
          muted
            ? 'text-muted-foreground'
            : selected && tone === 'red'
              ? 'text-[#a52f3d]'
              : 'text-[#31598e]'
        )}
      />
      <p
        className={cn(
          'mt-2 text-sm font-medium',
          muted ? 'text-[#5a6b85]' : 'text-[#2a4165]'
        )}
      >
        {title}
      </p>
      <p className='mt-1 text-xs text-muted-foreground'>{desc}</p>
    </button>
  )
}
