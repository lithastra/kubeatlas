import {workerData} from 'node:worker_threads';

export default function task(value) {
  return {marker: workerData[1].marker, task: value};
}
