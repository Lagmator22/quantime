FROM python:3.10-alpine
WORKDIR /app
COPY main.py .
RUN pip install aiohttp
EXPOSE 9001
CMD ["python", "main.py"]