import express from 'express';
import bodyParser from 'body-parser';
import cors from 'cors';
import { GcsExecutor } from './services/storage-executor';

const host = process.env.HOST ?? 'localhost';
const port = process.env.PORT ? Number(process.env.PORT) : 3000;

const app = express();

app.use(cors());
app.use(bodyParser.json({ limit: '100mb' }));
app.use(bodyParser.raw({ type: 'application/octet-stream', limit: '100mb' }));

app.get('/health', (req, res) => {
  res.send({ status: 'ok' });
});

/**
 * Generic GCS Command Execution Endpoint
 */
app.post('/gcs/v1/execute', async (req, res) => {
  console.log('POST /gcs/v1/execute', {
    method: req.body.method,
    bucket: req.body.bucket,
    file: req.body.file,
  });

  try {
    const result = await GcsExecutor.execute(req.body);
    res.json({ status: 200, data: result });
  } catch (err: any) {
    console.error('Execution error:', err);
    res.status(500).json({ status: 500, message: err.message });
  }
});

app.listen(port, host, () => {
  console.log(`[ ready ] http://${host}:${port}`);
});
