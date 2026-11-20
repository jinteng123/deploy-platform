import { describe, expect, it } from 'vitest'
import { promoteAction, releaseBuildIdentity } from './release-status'

describe('releaseBuildIdentity', () => {
  it('identifies a build by pipeline and build number', () => {
    expect(
      releaseBuildIdentity({
        pipeline_name: 'shop-pipeline',
        pipeline_id: 'pipeline-17',
        build_number: '88',
      })
    ).toBe('shop-pipeline (#pipeline-17) · 构建 #88')
  })

  it('keeps useful identifiers when pipeline metadata is incomplete', () => {
    expect(
      releaseBuildIdentity({
        pipeline_name: null,
        pipeline_id: 'pipeline-17',
        build_number: null,
      })
    ).toBe('流水线 #pipeline-17 · 构建号未知')
  })

  it('treats whitespace-only identifiers as missing', () => {
    expect(
      releaseBuildIdentity({
        pipeline_name: '  ',
        pipeline_id: '  ',
        build_number: '  ',
      })
    ).toBe('流水线未知 · 构建号未知')
  })
})

describe('promoteAction', () => {
  it('shows 已发布 once prod succeeded', () => {
    expect(
      promoteAction({
        can_promote: false,
        test_status: 'success',
        prod_status: 'success',
      })
    ).toEqual({
      label: '已发布',
      actionable: false,
    })
  })

  it('is actionable only when can_promote', () => {
    expect(
      promoteAction({
        can_promote: true,
        test_status: 'success',
        prod_status: null,
      })
    ).toEqual({
      label: '创建发布计划',
      actionable: true,
    })
    // prod failed retry is still actionable
    expect(
      promoteAction({
        can_promote: true,
        test_status: 'success',
        prod_status: 'failed',
      }).actionable
    ).toBe(true)
  })

  it('shows 等待测试环境 until the test deploy succeeds', () => {
    expect(
      promoteAction({
        can_promote: false,
        test_status: 'pending',
        prod_status: null,
      }).label
    ).toBe('等待测试环境')
    expect(
      promoteAction({
        can_promote: false,
        test_status: 'failed',
        prod_status: null,
      }).label
    ).toBe('等待测试环境')
  })

  it('shows 发布中 while a prod deploy is in flight', () => {
    expect(
      promoteAction({
        can_promote: false,
        test_status: 'success',
        prod_status: 'running',
      }).label
    ).toBe('发布中')
  })
})
