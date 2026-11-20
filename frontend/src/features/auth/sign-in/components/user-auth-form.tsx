import { useState } from 'react'
import { z } from 'zod'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { useNavigate } from '@tanstack/react-router'
import { CircleAlert, Loader2, LogIn } from 'lucide-react'
import { toast } from 'sonner'
import { authApi } from '@/api/client'
import { useAuthStore } from '@/stores/auth-store'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import {
  Form,
  FormControl,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { PasswordInput } from '@/components/password-input'

const formSchema = z.object({
  username: z.string().min(1, '请输入用户名。'),
  password: z.string().min(1, '请输入密码。'),
})

const INPUT_CLASSES =
  'h-11 rounded-lg border-[#d6e1f0] bg-[#fbfdff] focus-visible:border-[#234883] focus-visible:ring-[#234883]/20'
// PasswordInput renders its own inner <input>, so reach it via the wrapper.
const PASSWORD_INPUT_CLASSES =
  '[&_input]:h-11 [&_input]:rounded-lg [&_input]:border-[#d6e1f0] [&_input]:bg-[#fbfdff] [&_input]:focus-visible:border-[#234883] [&_input]:focus-visible:ring-[#234883]/20'

interface UserAuthFormProps extends React.HTMLAttributes<HTMLFormElement> {
  redirectTo?: string
}

export function UserAuthForm({
  className,
  redirectTo,
  ...props
}: UserAuthFormProps) {
  const [isLoading, setIsLoading] = useState(false)
  const [error, setError] = useState('')
  const navigate = useNavigate()
  const { auth } = useAuthStore()

  const form = useForm<z.infer<typeof formSchema>>({
    resolver: zodResolver(formSchema),
    defaultValues: {
      username: '',
      password: '',
    },
  })

  async function onSubmit(data: z.infer<typeof formSchema>) {
    setIsLoading(true)
    setError('')
    try {
      const res = await authApi.login(data.username, data.password)
      auth.setUser(res.data.user)
      toast.success(`欢迎回来，${res.data.user.display_name || res.data.user.username}`)
      const target =
        redirectTo?.startsWith('/') && !redirectTo.startsWith('/sign-in')
          ? redirectTo
          : '/'
      navigate({ to: target, replace: true })
    } catch (e) {
      const response = (e as { response?: { status?: number } })?.response
      const status = response?.status
      if (status === 401) {
        setError('用户名或密码错误')
      } else if (
        !response ||
        status === 502 ||
        status === 503 ||
        status === 504
      ) {
        setError('无法连接到平台服务，请确认后端已启动或稍后重试')
      } else {
        setError('登录失败，请稍后重试')
      }
    } finally {
      setIsLoading(false)
    }
  }

  return (
    <Form {...form}>
      <form
        onSubmit={form.handleSubmit(onSubmit)}
        className={cn('grid gap-5', className)}
        {...props}
      >
        <FormField
          control={form.control}
          name='username'
          render={({ field }) => (
            <FormItem>
              <FormLabel className='text-sm font-medium text-[#2a4165]'>
                用户名
              </FormLabel>
              <FormControl>
                <Input
                  placeholder='admin'
                  autoComplete='username'
                  autoFocus
                  className={INPUT_CLASSES}
                  {...field}
                />
              </FormControl>
              <FormMessage />
            </FormItem>
          )}
        />
        <FormField
          control={form.control}
          name='password'
          render={({ field }) => (
            <FormItem className='relative'>
              <FormLabel className='text-sm font-medium text-[#2a4165]'>
                密码
              </FormLabel>
              <FormControl>
                <PasswordInput
                  placeholder='********'
                  className={PASSWORD_INPUT_CLASSES}
                  {...field}
                />
              </FormControl>
              <FormMessage />
            </FormItem>
          )}
        />
        {error && (
          <p
            role='alert'
            className='flex items-start gap-2 rounded-lg bg-[#fdf0f1] px-3 py-2.5 text-sm text-[#a52f3d]'
          >
            <CircleAlert className='mt-0.5 size-4 shrink-0' />
            <span>{error}</span>
          </p>
        )}
        <Button
          className='mt-1 h-11 w-full rounded-lg bg-[#234883] text-white shadow-sm hover:bg-[#193d72]'
          disabled={isLoading}
        >
          {isLoading ? <Loader2 className='animate-spin' /> : <LogIn />}
          {isLoading ? '登录中…' : '登录'}
        </Button>
      </form>
    </Form>
  )
}
