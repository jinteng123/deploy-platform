/* eslint-disable react-refresh/only-export-components */
import { Link, createFileRoute } from '@tanstack/react-router'
import { ArrowLeft } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { DeliveryPlanDetailPage } from '@/features/platform/delivery-plan-detail'

function PlanDetailRoute() {
  const { planId } = Route.useParams()
  const planID = Number(planId)
  return (
    <DeliveryPlanDetailPage
      planID={planID}
      backLink={
        <Button asChild variant='ghost' size='sm' className='min-h-10 px-2'>
          <Link to='/tasks' aria-label='返回发布中心'>
            <ArrowLeft aria-hidden='true' />
            <span className='hidden sm:inline'>发布中心</span>
          </Link>
        </Button>
      }
    />
  )
}

export const Route = createFileRoute('/_authenticated/tasks/plans/$planId')({
  component: PlanDetailRoute,
})
