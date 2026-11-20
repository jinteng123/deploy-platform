import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, type RenderResult } from 'vitest-browser-react'
import { type Locator, userEvent } from 'vitest/browser'
import { authApi, type PublicUser } from '@/api/client'
import { UserAuthForm } from './user-auth-form'

const FORM_MESSAGES = {
  usernameEmpty: '请输入用户名。',
  passwordEmpty: '请输入密码。',
} as const

const navigate = vi.fn()
const setUserMock = vi.fn()

const adminUser: PublicUser = {
  id: 1,
  username: 'admin',
  display_name: 'Administrator',
  role: 'admin',
  k8s_username: 'admin',
  k8s_groups: [],
  enabled: true,
}

vi.mock('@/api/client', () => ({
  authApi: { login: vi.fn() },
}))

vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

vi.mock('@/stores/auth-store', () => ({
  useAuthStore: () => ({
    auth: { setUser: setUserMock },
  }),
}))

vi.mock('@tanstack/react-router', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@tanstack/react-router')>()
  return {
    ...actual,
    useNavigate: () => navigate,
    Link: ({
      children,
      to,
      className,
      ...rest
    }: {
      children?: React.ReactNode
      to: string
      className?: string
    }) => (
      <a href={to} className={className} {...rest}>
        {children}
      </a>
    ),
  }
})

const loginMock = vi.mocked(authApi.login)

describe('UserAuthForm', () => {
  describe('Rendering without redirectTo', () => {
    let screen: RenderResult
    let usernameInput: Locator
    let passwordInput: Locator
    let signInButton: Locator

    beforeEach(async () => {
      vi.clearAllMocks()
      loginMock.mockResolvedValue({ data: { user: adminUser } } as never)
      screen = await render(<UserAuthForm />)
      usernameInput = screen.getByRole('textbox', { name: /^用户名$/ })
      passwordInput = screen.getByLabelText(/^密码$/)
      signInButton = screen.getByRole('button', { name: /^登录$/ })
    })

    it('renders fields and submit button', async () => {
      await expect.element(usernameInput).toBeInTheDocument()
      await expect.element(passwordInput).toBeInTheDocument()
      await expect.element(signInButton).toBeInTheDocument()
    })

    it('shows validation messages when submitting empty form', async () => {
      await userEvent.click(signInButton)

      await expect
        .element(screen.getByText(FORM_MESSAGES.usernameEmpty))
        .toBeInTheDocument()
      await expect
        .element(screen.getByText(FORM_MESSAGES.passwordEmpty))
        .toBeInTheDocument()
    })

    it('calls the backend, stores the user, and navigates on success', async () => {
      await userEvent.fill(usernameInput, 'admin')
      await userEvent.fill(passwordInput, 'secret-pass')
      await userEvent.click(signInButton)

      await vi.waitFor(() => expect(loginMock).toHaveBeenCalledOnce())
      expect(loginMock).toHaveBeenCalledWith('admin', 'secret-pass')
      await vi.waitFor(() => expect(setUserMock).toHaveBeenCalledWith(adminUser))
      await vi.waitFor(() =>
        expect(navigate).toHaveBeenCalledWith({ to: '/', replace: true })
      )
    })

    it('rejects bad credentials without storing a user', async () => {
      loginMock.mockRejectedValue({ response: { status: 401 } })

      await userEvent.fill(usernameInput, 'admin')
      await userEvent.fill(passwordInput, 'wrong-pass')
      await userEvent.click(signInButton)

      await vi.waitFor(() => expect(loginMock).toHaveBeenCalledOnce())
      await expect
        .element(screen.getByRole('alert'))
        .toHaveTextContent('用户名或密码错误')
      expect(setUserMock).not.toHaveBeenCalled()
      expect(navigate).not.toHaveBeenCalled()
    })

    it('shows a connection message when there is no response', async () => {
      loginMock.mockRejectedValue(new Error('Network Error'))

      await userEvent.fill(usernameInput, 'admin')
      await userEvent.fill(passwordInput, 'secret-pass')
      await userEvent.click(signInButton)

      await vi.waitFor(() => expect(loginMock).toHaveBeenCalledOnce())
      await expect
        .element(screen.getByRole('alert'))
        .toHaveTextContent(
          '无法连接到平台服务，请确认后端已启动或稍后重试'
        )
      expect(setUserMock).not.toHaveBeenCalled()
      expect(navigate).not.toHaveBeenCalled()
    })

    it('shows a connection message on a 502 through the proxy', async () => {
      loginMock.mockRejectedValue({ response: { status: 502 } })

      await userEvent.fill(usernameInput, 'admin')
      await userEvent.fill(passwordInput, 'secret-pass')
      await userEvent.click(signInButton)

      await vi.waitFor(() => expect(loginMock).toHaveBeenCalledOnce())
      await expect
        .element(screen.getByRole('alert'))
        .toHaveTextContent(
          '无法连接到平台服务，请确认后端已启动或稍后重试'
        )
      expect(setUserMock).not.toHaveBeenCalled()
      expect(navigate).not.toHaveBeenCalled()
    })
  })

  it('navigates to redirectTo when provided', async () => {
    vi.clearAllMocks()
    loginMock.mockResolvedValue({ data: { user: adminUser } } as never)

    const { getByRole, getByLabelText } = await render(
      <UserAuthForm redirectTo='/settings' />
    )

    await userEvent.fill(getByRole('textbox', { name: /用户名/ }), 'admin')
    await userEvent.fill(getByLabelText('密码'), 'secret-pass')
    await userEvent.click(getByRole('button', { name: /登录/ }))

    await vi.waitFor(() => expect(setUserMock).toHaveBeenCalledOnce())
    await vi.waitFor(() =>
      expect(navigate).toHaveBeenCalledWith({
        to: '/settings',
        replace: true,
      })
    )
  })

  it('ignores redirectTo values that point back at sign-in', async () => {
    vi.clearAllMocks()
    loginMock.mockResolvedValue({ data: { user: adminUser } } as never)

    const { getByRole, getByLabelText } = await render(
      <UserAuthForm redirectTo='/sign-in?redirect=%2Fsign-in' />
    )

    await userEvent.fill(getByRole('textbox', { name: /用户名/ }), 'admin')
    await userEvent.fill(getByLabelText('密码'), 'secret-pass')
    await userEvent.click(getByRole('button', { name: /登录/ }))

    await vi.waitFor(() => expect(setUserMock).toHaveBeenCalledOnce())
    await vi.waitFor(() =>
      expect(navigate).toHaveBeenCalledWith({ to: '/', replace: true })
    )
  })
})
