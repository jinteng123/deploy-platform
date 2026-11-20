"""
入口文件：uvicorn 直接运行
  uvicorn run:app --host 0.0.0.0 --port 8000 --reload
"""
from app.main import create_app

app = create_app()
