"""
APScheduler 初始化：注册定时清理任务
"""
from apscheduler.schedulers.background import BackgroundScheduler
from apscheduler.triggers.cron import CronTrigger

from app.tasks.cleanup import cleanup_old_files

scheduler = BackgroundScheduler(timezone="Asia/Shanghai")


def start_scheduler():
    scheduler.add_job(
        cleanup_old_files,
        trigger=CronTrigger(hour=2, minute=0),
        id="cleanup_transfer_space",
        name="每日清理过期文件包",
        replace_existing=True,
    )
    scheduler.start()


def stop_scheduler():
    scheduler.shutdown(wait=False)
